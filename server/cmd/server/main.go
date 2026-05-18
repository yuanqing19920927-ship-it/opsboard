package main

import (
	"flag"
	"log"
	"os"

	"database/sql"

	"mantisops/server/internal/ai"
	"mantisops/server/internal/alert"
	"mantisops/server/internal/api"
	"mantisops/server/internal/cloud"
	"mantisops/server/internal/collector"
	"mantisops/server/internal/config"
	"mantisops/server/internal/crypto"
	"mantisops/server/internal/deployer"
	grpcpkg "mantisops/server/internal/grpc"
	"mantisops/server/internal/logging"
	"mantisops/server/internal/network"
	"mantisops/server/internal/probe"
	"mantisops/server/internal/store"
	"mantisops/server/internal/ws"
)

func main() {
	cfgPath := flag.String("config", "configs/server.yaml", "config file path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// 1. SQLite (with foreign_keys)
	os.MkdirAll("data", 0755)
	db, err := store.InitSQLite(cfg.SQLite.Path)
	if err != nil {
		log.Fatalf("init sqlite: %v", err)
	}
	defer db.Close()

	// 2. Encryption key
	masterKey, err := crypto.EnsureKey(cfg.EncryptionKey, *cfgPath)
	if err != nil {
		log.Fatalf("encryption key: %v", err)
	}

	// 3. Stores
	serverStore := store.NewServerStore(db)
	groupStore := store.NewGroupStore(db)
	credentialStore := store.NewCredentialStore(db, masterKey)
	cloudStore := store.NewCloudStore(db)
	managedServerStore := store.NewManagedServerStore(db)
	nasStore := store.NewNasStore(db)

	// 4. VictoriaMetrics
	vmStore := store.NewVictoriaStore(cfg.Victoria.URL)

	// 5. Logging system (logs.db + LogManager)
	os.MkdirAll(cfg.Logging.Dir, 0755)
	logsDB, err := sql.Open("sqlite", cfg.Logging.Dir+"/logs.db")
	if err != nil {
		log.Fatalf("open logs.db: %v", err)
	}
	defer logsDB.Close()
	logsDB.Exec("PRAGMA journal_mode=WAL")

	logStore, err := logging.NewLogStore(logsDB)
	if err != nil {
		log.Fatalf("init log store: %v", err)
	}

	// 6. WebSocket Hub
	hub := ws.NewHub()

	logMgr, err := logging.NewLogManager(logStore, hub, logging.ParseLevel(cfg.Logging.Level), cfg.Logging.Dir)
	if err != nil {
		log.Fatalf("init log manager: %v", err)
	}
	defer logMgr.Close()

	// Hook standard logger so all existing log.Printf calls flow through LogManager
	logMgr.HookStdLog()

	// Log cleaner
	logCleaner := logging.NewCleaner(cfg.Logging.Dir, logStore, cfg.Logging.Retention, cfg.Logging.CleanupHour)
	logCleaner.Start()
	defer logCleaner.Stop()

	// 6. Metrics Collector
	mc := collector.NewMetricsCollector(vmStore, hub, serverStore)

	// 7. Cloud Manager
	cloudManager := cloud.NewManager(db, cloudStore, credentialStore, hub)

	// 8. Deployer
	binaryDir := cfg.AgentBin.BinaryDir
	if binaryDir == "" {
		binaryDir = "./build"
	}
	registerTimeout := cfg.AgentBin.RegisterTimeout
	if registerTimeout <= 0 {
		registerTimeout = 120
	}
	grpcAdvertise := cfg.Server.GRPCAdvertiseAddr
	if grpcAdvertise == "" {
		grpcAdvertise = cfg.Server.GRPCAddr
	}
	dep := deployer.NewDeployer(
		managedServerStore, credentialStore, serverStore, hub,
		cfg.Server.PSKToken, grpcAdvertise, binaryDir, registerTimeout,
	)
	dep.RecoverStaleWaiting() // fix any managed servers stuck in "waiting" state

	// 8.5. User store + permission cache (needed by handlers below)
	userStore := store.NewUserStore(db)
	permCache := api.NewPermissionCache(userStore, serverStore)

	// 9. Probe
	probeStore := store.NewProbeStore(db)
	prober := probe.NewProber(probeStore, vmStore, hub)
	prober.Start(cfg.Probe.Interval)
	defer prober.Stop()
	probeHandler := api.NewProbeHandler(probeStore, prober, permCache)

	// Scan
	scanTemplateStore := store.NewScanTemplateStore(db)
	scanner := probe.NewScanner(probeStore, scanTemplateStore, hub)
	scanHandler := api.NewScanHandler(scanTemplateStore, serverStore, scanner)

	// NAS Collector
	nasCollector := collector.NewNasCollector(nasStore, credentialStore, vmStore, hub)
	nasCollector.Start()
	defer nasCollector.Stop()

	// 10. Alert system
	// networkStore is created here (early) so it can be passed to the alerter
	// for network_device_offline rule evaluation.  The full network topology
	// module (scanner, monitor, handler) is initialised later at step 18.
	alertNetworkStore := store.NewNetworkStore(db)
	// dbHandler is created here (early) so it can serve as the alerter's
	// DatabaseProvider for db_* (RDS) rule evaluation. Its API routes are
	// wired later via RouterDeps (step 22).
	dbHandler := api.NewDatabaseHandler(cloudStore, vmStore, permCache)
	alertStore := store.NewAlertStore(db)
	alerter := alert.NewAlerter(alertStore, hub, mc, prober, serverStore, nasCollector, alertNetworkStore, dbHandler)
	alerter.Start()
	defer alerter.Stop()
	alertHandler := api.NewAlertHandler(alertStore, alerter, permCache)

	// Pre-load firing alert targets into Hub for WS permission filtering
	if targets, err := alertStore.ListFiringAlertTargets(); err == nil && len(targets) > 0 {
		m := make(map[int]ws.AlertTarget, len(targets))
		for _, t := range targets {
			m[t.EventID] = ws.AlertTarget{RuleType: t.RuleType, TargetID: t.TargetID}
		}
		hub.LoadAlertTargets(m)
		log.Printf("loaded %d firing alert targets into Hub", len(targets))
	}

	// 11. Asset + Discovered Services
	assetStore := store.NewAssetStore(db)
	discoveredServiceStore := store.NewDiscoveredServiceStore(db)
	assetHandler := api.NewAssetHandler(assetStore, discoveredServiceStore, serverStore, permCache)

	// 12. Aliyun Cloud Collector
	var metricsProvider api.MetricsProvider
	// Start collector if enabled in config OR if cloud accounts exist in DB
	shouldStartCollector := cfg.Aliyun.Enabled
	if !shouldStartCollector {
		if accounts, err := cloudStore.ListAccounts(); err == nil && len(accounts) > 0 {
			shouldStartCollector = true
		}
	}
	if shouldStartCollector {
		ac, err := collector.NewAliyunCollector(cfg.Aliyun, vmStore, serverStore, hub, cloudStore, credentialStore)
		if err != nil {
			log.Printf("aliyun collector init failed: %v", err)
		} else {
			if migratedID := ac.MigrateFromConfig(); migratedID > 0 {
				// Sync to fetch instance metadata (names, engine, spec, endpoint)
				cloudManager.Sync(migratedID)
			}
			ac.Start()
			defer ac.Stop()
			metricsProvider = ac
			log.Printf("aliyun collector started")
		}
	}

	// 13. gRPC with deployer callback — 拒绝空 PSK 启动
	if cfg.Server.PSKToken == "" {
		log.Fatalf("FATAL: server.psk_token must be configured (non-empty)")
	}
	handler := grpcpkg.NewHandler(serverStore, mc.Handle, dep.NotifyRegistered, discoveredServiceStore, assetStore)
	psk := grpcpkg.NewPSKInterceptor(cfg.Server.PSKToken)
	go func() {
		if err := grpcpkg.StartPlain(cfg.Server.GRPCAddr, handler, psk); err != nil {
			log.Fatalf("gRPC error: %v", err)
		}
	}()

	// 14. Auth — multi-user with bcrypt
	if cfg.Auth.JWTSecret == "" {
		log.Fatalf("FATAL: auth.jwt_secret must be configured (non-empty, recommend 32+ random chars)")
	}
	tvCache := api.NewTokenVersionCache(userStore)

	// Migrate initial admin from server.yaml (one-time)
	if hasUser, _ := userStore.HasAnyUser(); !hasUser {
		if cfg.Auth.Username != "" && cfg.Auth.Password != "" {
			hash, err := api.HashPassword(cfg.Auth.Password)
			if err != nil {
				log.Fatalf("hash initial admin password: %v", err)
			}
			if _, err := userStore.CreateInitialAdmin(cfg.Auth.Username, hash); err != nil {
				log.Fatalf("create initial admin: %v", err)
			}
			log.Printf("migrated initial admin user '%s' from config", cfg.Auth.Username)
		} else {
			log.Printf("WARNING: no users in database and auth.username/password not configured")
		}
	}

	authHandler := api.NewAuthHandler(userStore, cfg.Auth.JWTSecret, tvCache)
	userHandler := api.NewUserHandler(userStore, tvCache, permCache, hub)

	// 15. Database (RDS) handler created early at step 10 (alerter's
	// DatabaseProvider); routes wired via RouterDeps at step 22.

	// 16. Billing - reads from CloudStore + CredentialStore
	billingHandler := api.NewBillingHandler(cloudStore, credentialStore, cfg.Aliyun, permCache)

	// 17. Groups
	groupHandler := api.NewGroupHandler(groupStore, serverStore, permCache)

	// 18. New handlers
	credentialHandler := api.NewCredentialHandler(credentialStore)
	cloudHandler := api.NewCloudHandler(cloudManager, cloudStore, credentialStore)
	managedServerHandler := api.NewManagedServerHandler(managedServerStore, dep, credentialStore)

	// NAS handler
	nasHandler := api.NewNasHandler(nasStore, credentialStore, nasCollector)

	// Network topology module (reuse the store created earlier for alerter)
	networkStore := alertNetworkStore
	var networkHandler *api.NetworkHandler
	var networkMonitor *network.ConnectivityMonitor
	if cfg.Network.Enabled {
		netScanner := network.NewScanner(cfg.Network, hub)
		snmpProber := network.NewSNMPProber(cfg.Network.SNMPCommunities, cfg.Network.Scan.SNMPTimeoutMs)
		networkHandler = api.NewNetworkHandler(networkStore, netScanner, snmpProber, hub, credentialStore, serverStore)
		networkMonitor = network.NewConnectivityMonitor(cfg.Network, networkStore, hub)
		networkMonitor.Start()
		defer networkMonitor.Stop()
		log.Println("[network] topology module enabled")
	}

	// 19. Log handler
	logSearcher := logging.NewLogSearcher(logStore, cfg.Logging.Dir)
	logHandler := api.NewLogHandler(logStore, logSearcher, permCache)

	// 20. Settings
	settingsStore := store.NewSettingsStore(db)
	settingsHandler := api.NewSettingsHandler(settingsStore)

	// 21. AI system — always initialized, provider config comes from DB settings
	aiStore := store.NewAIStore(db)
	providerMgr := ai.NewProviderManager(&cfg.AI, settingsStore, masterKey)
	dataCollector := ai.NewDataCollector(cfg.Victoria.URL, serverStore, alertStore, cfg.AI.Timezone)
	reporter := ai.NewReporter(aiStore, dataCollector, providerMgr, hub, settingsStore, cfg.AI)
	chatEngine := ai.NewChatEngine(aiStore, providerMgr, dataCollector, hub, cfg.AI.Chat)
	scheduler := ai.NewScheduler(aiStore, reporter, cfg.AI.Timezone)

	hub.SetOnAIStreamSubscribe(chatEngine.OnStreamSubscribe)

	scheduler.Start()
	defer scheduler.Stop()

	aiHandler := api.NewAIHandler(aiStore, reporter, chatEngine, providerMgr, scheduler, settingsStore, masterKey)
	log.Println("AI analysis module initialized")

	// 22. HTTP API
	router := api.SetupRouter(api.RouterDeps{
		ServerStore:          serverStore,
		GroupStore:           groupStore,
		Hub:                  hub,
		MetricsProvider:      metricsProvider,
		StaticDir:            cfg.Server.StaticDir,
		ProbeHandler:         probeHandler,
		AssetHandler:         assetHandler,
		AuthHandler:          authHandler,
		DatabaseHandler:      dbHandler,
		BillingHandler:       billingHandler,
		AlertHandler:         alertHandler,
		GroupHandler:         groupHandler,
		CredentialHandler:    credentialHandler,
		CloudHandler:         cloudHandler,
		ManagedServerHandler: managedServerHandler,
		NasHandler:           nasHandler,
		NetworkHandler:       networkHandler,
		LogHandler:           logHandler,
		LogManager:           logMgr,
		ScanHandler:          scanHandler,
		SettingsHandler:      settingsHandler,
		UserHandler:          userHandler,
		PermissionCache:      permCache,
		AIHandler:            aiHandler,
	})
	log.Printf("HTTP server on %s, gRPC on %s", cfg.Server.HTTPAddr, cfg.Server.GRPCAddr)
	if err := router.Run(cfg.Server.HTTPAddr); err != nil {
		log.Fatalf("HTTP error: %v", err)
	}
}
