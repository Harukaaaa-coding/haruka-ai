package main

import (
	"GopherAI/common/health"
	"GopherAI/common/mysql"
	"GopherAI/common/rabbitmq"
	"GopherAI/common/redis"
	"GopherAI/config"
	"GopherAI/router"
	agentservice "GopherAI/service/agent"
	imageservice "GopherAI/service/image"
	knowledgebaseservice "GopherAI/service/knowledgebase"
	mcphubservice "GopherAI/service/mcphub"
	userservice "GopherAI/service/user"
	voiceconversation "GopherAI/service/voiceconversation"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func StartServer(ctx context.Context, addr string, port int, checker *health.Checker) error {
	if checker == nil {
		checker = health.NewChecker(time.Second)
	}
	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", addr, port),
		Handler:           router.InitRouter(checker),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", server.Addr, err)
	}
	return runHTTPServer(ctx, server, listener, checker, 10*time.Second)
}

func runHTTPServer(ctx context.Context, server *http.Server, listener net.Listener, checker *health.Checker, gracePeriod time.Duration) error {
	if server == nil {
		return errors.New("HTTP server is nil")
	}
	if listener == nil {
		return errors.New("HTTP listener is nil")
	}
	if checker == nil {
		checker = health.NewChecker(time.Second)
	}
	if gracePeriod <= 0 {
		gracePeriod = 10 * time.Second
	}
	checker.MarkReady()
	defer checker.MarkNotReady()

	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		checker.MarkNotReady()
		// WebSockets are upgraded/hijacked connections and are not governed by
		// http.Server.Shutdown. Stop accepting them first, then cancel active
		// ASR/LLM/TTS turns while the normal HTTP server drains.
		voiceHub := voiceconversation.DefaultHub()
		voiceHub.BeginDrain()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), gracePeriod)
		shutdownErr := server.Shutdown(shutdownCtx)
		voiceDrainErr := voiceHub.Wait(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			closeErr := server.Close()
			listenErr := <-errCh
			if errors.Is(closeErr, http.ErrServerClosed) {
				closeErr = nil
			}
			if errors.Is(listenErr, http.ErrServerClosed) {
				listenErr = nil
			}
			return errors.Join(fmt.Errorf("shutdown HTTP server: %w", shutdownErr), voiceDrainErr, closeErr, listenErr)
		}
		listenErr := <-errCh
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			return errors.Join(voiceDrainErr, listenErr)
		}
		return voiceDrainErr
	}
}

func readinessChecker() *health.Checker {
	return health.NewChecker(time.Second,
		health.Check{Name: "mysql", Required: true, Run: mysql.Ping},
		health.Check{Name: "redis", Required: true, Run: redis.Ping},
		health.Check{Name: "rabbitmq", Run: func(context.Context) error {
			if !rabbitmq.Available() {
				return errors.New("RabbitMQ is unavailable")
			}
			return nil
		}},
		health.Check{Name: "agent_worker", Run: func(context.Context) error {
			if !agentservice.WorkerRunning() {
				return errors.New("Agent worker is stopped")
			}
			return nil
		}},
		health.Check{Name: "knowledge_worker", Run: func(context.Context) error {
			if !knowledgebaseservice.IndexWorkerRunning() {
				return errors.New("knowledge worker is stopped")
			}
			return nil
		}},
		health.Check{Name: "registration_email_worker", Run: func(context.Context) error {
			if !userservice.RegistrationEmailWorkerRunning() {
				return errors.New("registration email worker is stopped")
			}
			return nil
		}},
	)
}

func shutdownRuntime(ctx context.Context) {
	var waitGroup sync.WaitGroup
	waitFor := func(name string, wait func(context.Context) error) {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := wait(ctx); err != nil {
				log.Printf("stop %s: %v", name, err)
			}
		}()
	}
	waitFor("Agent worker", agentservice.WaitWorker)
	waitFor("knowledge worker", knowledgebaseservice.WaitIndexWorker)
	waitFor("registration email worker", userservice.WaitRegistrationEmailWorker)
	waitFor("RabbitMQ consumer", rabbitmq.ShutdownRabbitMQ)
	waitGroup.Wait()
	if err := imageservice.CloseContext(ctx); err != nil {
		log.Printf("close image recognizer: %v", err)
	}
	if ctx.Err() != nil {
		// A worker may still be using shared clients. Returning from main lets the
		// OS reclaim them without racing an in-flight database or Redis call.
		log.Printf("runtime shutdown deadline reached; skipping shared-client close: %v", ctx.Err())
		return
	}

	if err := mcphubservice.CloseDefault(); err != nil {
		log.Printf("close MCP Hub: %v", err)
	}
	if err := redis.Close(); err != nil {
		log.Printf("close Redis: %v", err)
	}
	if err := mysql.Close(); err != nil {
		log.Printf("close MySQL: %v", err)
	}
}

func main() {
	appCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conf := config.GetConfig()
	if err := config.GetConfigError(); err != nil {
		log.Printf("configuration is invalid: %v", err)
		return
	}
	host := conf.MainConfig.Host
	port := conf.MainConfig.Port
	//初始化mysql
	if err := mysql.InitMysql(); err != nil {
		log.Println("InitMysql error , " + err.Error())
		return
	}
	//初始化redis
	if err := redis.Init(appCtx); err != nil {
		log.Printf("InitRedis error: %v", err)
		if closeErr := mysql.Close(); closeErr != nil {
			log.Printf("close MySQL after Redis failure: %v", closeErr)
		}
		return
	}
	userservice.StartRegistrationEmailWorker(appCtx)
	log.Println("redis init success  ")
	if err := rabbitmq.InitRabbitMQ(appCtx); err != nil {
		log.Printf("rabbitmq unavailable; messages will be persisted directly to MySQL: %v", err)
	} else {
		log.Println("rabbitmq init success")
	}
	if err := knowledgebaseservice.StartIndexWorker(appCtx); err != nil {
		log.Printf("Knowledge worker unavailable; indexing remains pending: %v", err)
	} else {
		log.Println("Knowledge worker init success")
	}
	if err := agentservice.StartWorker(appCtx); err != nil {
		log.Printf("Agent worker unavailable; Agent tasks cannot run: %v", err)
	} else {
		log.Println("Agent worker init success")
	}

	checker := readinessChecker()
	err := StartServer(appCtx, host, port, checker) // 启动 HTTP 服务
	if err != nil {
		log.Printf("HTTP server stopped: %v", err)
	}
	checker.MarkNotReady()
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	shutdownRuntime(shutdownCtx)
	cancel()
}
