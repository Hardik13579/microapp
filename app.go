package microapp

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"gorm.io/gorm/schema"

	"time"

	memcache "github.com/bradfitz/gomemcache/memcache"
	migrate "github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/file"
	"github.com/gorilla/mux"
	"github.com/islax/microapp/config"
	microappCtx "github.com/islax/microapp/context"
	"github.com/islax/microapp/event"
	"github.com/islax/microapp/log"
	"github.com/islax/microapp/repository"
	"github.com/islax/microapp/retry"
	"github.com/islax/microapp/security"
	gormmysqldriver "gorm.io/driver/mysql"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"

	"github.com/rs/zerolog"
	uuid "github.com/satori/go.uuid"
)

// RouteSpecifier should be implemented by the class that sets routes for the API endpoints
type RouteSpecifier interface {
	RegisterRoutes(router *mux.Router)
}

// App structure for tenant microservice
type App struct {
	Name            string
	Config          *config.Config
	DB              *gorm.DB
	MemcacheClient  *memcache.Client
	Router          *mux.Router
	server          *http.Server
	log             zerolog.Logger
	eventDispatcher event.Dispatcher
}

// NewWithEnvValues creates a new application with environment variable values for initializing database, event dispatcher and logger.
func NewWithEnvValues(appName string, appConfigDefaults map[string]interface{}) *App {
	appConfig := config.NewConfig(appConfigDefaults)
	log.InitializeGlobalSettings()
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	consoleOnlyLogger := log.New(appName, appConfig.GetString("LOG_LEVEL"), consoleWriter)
	consoleOnlyLogger.Info().Msgf("Staring: %v", appName)
	// consoleOnlyLogger := zerolog.New(consoleWriter).With().Timestamp().Str("service", appName).Logger().Level()

	multiWriters := io.MultiWriter(consoleWriter)
	var err error
	var appEventDispatcher event.Dispatcher
	if appConfig.GetStringWithDefault("ENABLE_EVENT_DISPATCHER", "0") == "1" || appConfig.GetStringWithDefault("LOG_TO_EVENTQ", "0") == "1" {
		if appEventDispatcher, err = event.NewRabbitMQEventDispatcher(consoleOnlyLogger); err != nil {
			consoleOnlyLogger.Fatal().Err(err).Msg("Failed to initialize event dispatcher to queue, exiting the application!")
		}
		if appConfig.GetStringWithDefault("LOG_TO_EVENTQ", "0") == "1" {
			multiWriters = io.MultiWriter(consoleWriter, event.NewEventQWriter(appEventDispatcher))
		}
	} else {
		consoleOnlyLogger.Warn().Msg("Event dispatcher not enabled. Please set ISLA_ENABLE_EVENT_DISPATCHER or ISLA_LOG_TO_EVENTQ to '1' to enable it.")
	}
	//TODO: default module to system
	appLogger := log.New(appName, appConfig.GetString("LOG_LEVEL"), multiWriters)
	//TODO: Need to wait till eventDispatcher is ready
	time.Sleep(5 * time.Second)

	app := App{Name: appName, Config: appConfig, log: *appLogger, eventDispatcher: appEventDispatcher}
	err = app.initializeDB()
	if err != nil {
		consoleOnlyLogger.Fatal().Err(err).Msg("Failed to initialize database, exiting the application!!")
	}
	app.initializeMemcache()
	return &app
}

// New creates a new microApp
func New(appName string, appConfigDefaults map[string]interface{}, appLog zerolog.Logger, appDB *gorm.DB, appMemcache *memcache.Client, appEventDispatcher event.Dispatcher) *App {
	appConfig := config.NewConfig(appConfigDefaults)
	return &App{Name: appName, Config: appConfig, log: appLog, DB: appDB, MemcacheClient: appMemcache, eventDispatcher: appEventDispatcher}
}

func (app *App) initializeDB() error {
	if app.Config.GetBool(config.EvSuffixForDBRequired) {
		var db *gorm.DB
		err := retry.Do(3, time.Second*15, func() error {
			var err error
			dbconf := &gorm.Config{PrepareStmt: true}

			switch strings.ToLower(app.Config.GetString(config.EvSuffixForDBLogLevel)) {
			case "info":
				dbconf.Logger = glogger.Default.LogMode(glogger.Info)
			case "warn":
				dbconf.Logger = glogger.Default.LogMode(glogger.Warn)
			case "error":
				dbconf.Logger = glogger.Default.LogMode(glogger.Error)
			case "silent":
				dbconf.Logger = glogger.Default.LogMode(glogger.Silent)
			default:
				if strings.ToLower(app.Config.GetString(config.EvSuffixForLogLevel)) == "trace" {
					dbconf.Logger = glogger.Default.LogMode(glogger.Info)
				} else {
					dbconf.Logger = glogger.Default.LogMode(glogger.Error)
				}
			}

			if app.Config.GetBool("DB_NAMING_STRATEGY_IS_SINGULAR") {
				dbconf.NamingStrategy = schema.NamingStrategy{SingularTable: true}
			}

			sqlDB, err := sql.Open("mysql", app.GetConnectionString())
			if err != nil {
				app.log.Error().Err(err).Msgf("Error creating connection pool [%v]. Trying again...", err)
			}
			sqlDB.SetConnMaxLifetime(time.Duration(app.Config.GetInt(config.EvSuffixForDBConnectionLifetime)) * time.Minute)
			sqlDB.SetMaxIdleConns(app.Config.GetInt(config.EvSuffixForDBMaxIdleConnections))

			db, err = gorm.Open(gormmysqldriver.New(gormmysqldriver.Config{
				Conn: sqlDB,
			}), dbconf)
			if err != nil && strings.Contains(err.Error(), "connection refused") {
				app.log.Warn().Msgf("Error connecting to Database [%v]. Trying again...", err)
				return err
			}

			return retry.Stop{OriginalError: err}
		})
		app.DB = db
		app.log.Info().Msg("Database connected!")
		return err
	}
	return nil
}

// GetConnectionString gets database connection string
func (app *App) GetConnectionString() string {
	dbHost := app.Config.GetString("DB_HOST")
	dbName := app.Config.GetString("DB_NAME")
	dbPort := app.Config.GetString("DB_PORT")
	dbUser := app.Config.GetString("DB_USER")
	dbPassword := app.Config.GetString("DB_PWD")

	return fmt.Sprintf("%v:%v@tcp(%v:%v)/%v?multiStatements=true&charset=utf8&parseTime=True&loc=Local", dbUser, dbPassword, dbHost, dbPort, dbName)
}

// NewUnitOfWork creates new UnitOfWork
func (app *App) NewUnitOfWork(readOnly bool) *repository.UnitOfWork {
	return repository.NewUnitOfWork(app.DB, readOnly)
}

//Initialize initializes properties of the app
func (app *App) Initialize(routeSpecifiers []RouteSpecifier) {
	logger := app.log
	app.Router = mux.NewRouter()
	app.Router.Use(mux.CORSMethodMiddleware(app.Router))
	app.Router.Use(app.loggingMiddleware)

	for _, routeSpecifier := range routeSpecifiers {
		routeSpecifier.RegisterRoutes(app.Router)
	}

	//TODO: Revisit this logic
	apiPort := "80"
	if app.Config.IsSet("API_PORT") {
		port := app.Config.GetString("API_PORT")
		if _, err := strconv.Atoi(port); err != nil {
			logger.Error().Msg("API port needs to be a number. " + port + " is not a number.")
		} else {
			apiPort = port
		}
	}

	logger.Debug().Str("appname", app.Name).Msg("Api server will start on port: " + apiPort)
	app.server = &http.Server{
		Addr:         "0.0.0.0:" + apiPort,
		WriteTimeout: time.Second * time.Duration(app.Config.GetInt("HTTP_WRITE_TIMEOUT")),
		ReadTimeout:  time.Second * time.Duration(app.Config.GetInt("HTTP_READ_TIMEOUT")),
		IdleTimeout:  time.Second * time.Duration(app.Config.GetInt("HTTP_IDLE_TIMEOUT")),
		Handler:      app.Router,
	}
}

//Start http server and start listening to the requests
func (app *App) Start() {
	if app.Config.GetString("ENABLE_TLS") == "true" {
		app.StartSecure(app.Config.GetString("TLS_CRT"), app.Config.GetString("TLS_KEY"))
	} else {
		if err := app.server.ListenAndServe(); err != nil {
			if err != http.ErrServerClosed {
				app.log.Fatal().Err(err).Msg("Unable to start server, exiting the application!")
			}
		}
	}
}

//StartSecure starts https server and listens to the requests
func (app *App) StartSecure(tlsCert string, tlsKey string) {

	if tlsCert == "" {
		app.log.Fatal().Msg("TLS_CRT is not defined or empty, exiting the application!")
	}

	if tlsKey == "" {
		app.log.Fatal().Msg("TLS_KEY is not defined or empty, exiting the application!")
	}

	if err := app.server.ListenAndServeTLS(tlsCert, tlsKey); err != nil {
		app.log.Fatal().Err(err).Msg("Unable to start server or server stopped, exiting the application!")
	}
}

// Logger returns logger for specified module
func (app *App) Logger(module string) *zerolog.Logger {
	logger := app.log.With().Str("service", app.Name).Str("module", module).Logger()
	return &logger
}

// MigrateDB Looks for migrations directory and runs the migrations scripts in that directory
func (app *App) MigrateDB() {
	logger := app.log

	logger.Debug().Msg("DB Migration Begin...")
	fsrc, err := (&file.File{}).Open("file://migrations")
	if err != nil {
		logger.Info().Err(err).Msg("No migrations directory found, skipping migrations!")
		logger.Info().Msg("DB Migration End!")
		return
	}
	migrateDB, err := sql.Open("mysql", app.GetConnectionString())
	if err != nil {
		logger.Fatal().Err(err).Msg("Unable to open DB connection for migration, exiting the application!")
	}
	migrateDBDriver, err := mysql.WithInstance(migrateDB, &mysql.Config{})
	if err != nil {
		logger.Fatal().Err(err).Msg("Unable to prepare DB instance for migration, exiting the application!")
	}
	m, err := migrate.NewWithInstance("file", fsrc, "mysql", migrateDBDriver)
	if err != nil {
		logger.Fatal().Err(err).Msg("Unable to initialize DB instance for migration, exiting the application!")
	}
	err = m.Up()
	if err != nil {
		if err.Error() == "no change" {
			logger.Info().Msg("DB already in latest state.")
		} else {
			logger.Fatal().Err(err).Msg("Failed to migrate DB, exiting the application!")
		}
	} else {
		logger.Debug().Msg("Successfully upgraded DB")
	}
	logger.Info().Msg("DB Migration End!")
}

// Stop http server
func (app *App) Stop() {
	wait, _ := time.ParseDuration("2m")
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	app.server.Shutdown(ctx)

	if app.Config.GetBool("DB_REQUIRED") {
		sqlDB, err := app.DB.DB()
		if err != nil {
			sqlDB.Close()
		}
	}
}

type httpStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *httpStatusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func (app *App) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()

		if r.Header.Get("X-Correlation-ID") == "" {
			r.Header.Set("X-Correlation-ID", uuid.NewV4().String())
		}
		logger := app.Logger("Ingress").With().Timestamp().Str("caller", r.Header.Get("X-Client")).Str("correlationId", r.Header.Get("X-Correlation-ID")).Str("method", r.Method).Str("requestURI", r.RequestURI).Logger()

		rec := &httpStatusRecorder{ResponseWriter: w}
		logger.Info().Msg("Begin")
		next.ServeHTTP(rec, r)
		if rec.status >= http.StatusInternalServerError {
			logger.Error().Int("status", rec.status).Dur("responseTime", time.Now().Sub(startTime)).Msg("End.")
		} else {
			logger.Info().Int("status", rec.status).Dur("responseTime", time.Now().Sub(startTime)).Msg("End.")
		}
	})
}

// DispatchEvent delegates to eventDispatcher.
func (app *App) DispatchEvent(token string, corelationID string, topic string, payload interface{}) {
	if app.eventDispatcher != nil {
		app.eventDispatcher.DispatchEvent(token, corelationID, topic, payload)
	}
}

// NewExecutionContext creates new exectuion context
func (app *App) NewExecutionContext(uow *repository.UnitOfWork, token *security.JwtToken, correlationID string, action string) microappCtx.ExecutionContext {
	return microappCtx.NewExecutionContext(token, uow, correlationID, action, app.log)
}

// NewExecutionContextWithCustomToken creates new exectuion context with custom made token
func (app *App) NewExecutionContextWithCustomToken(uow *repository.UnitOfWork, tenantID uuid.UUID, userID uuid.UUID, username string, correlationID string, action string, admin bool) microappCtx.ExecutionContext {
	return microappCtx.NewExecutionContext(&security.JwtToken{Admin: admin, TenantID: tenantID, UserID: userID, UserName: username}, uow, correlationID, action, app.log)
}

// NewExecutionContextWithSystemToken creates new exectuion context with sys default token
func (app *App) NewExecutionContextWithSystemToken(uow *repository.UnitOfWork, correlationID string, action string, admin bool) microappCtx.ExecutionContext {
	return microappCtx.NewExecutionContext(&security.JwtToken{Admin: admin, TenantID: uuid.Nil, UserID: uuid.Nil, TenantName: "None", UserName: "System", DisplayName: "System"}, uow, correlationID, action, app.log)
}

// GetCorrelationIDFromRequest returns correlationId from request header
func GetCorrelationIDFromRequest(r *http.Request) string {
	return r.Header.Get("X-Correlation-ID")
}

// initializeMemcache returns connection client for Memcached
func (app *App) initializeMemcache() {
	memcachedHost := app.Config.GetString("MEMCACHED_HOST")
	memcachedPort := app.Config.GetString("MEMCACHED_PORT")
	memcahched := memcache.New(memcachedHost + ":" + memcachedPort)
	app.MemcacheClient = memcahched
}
