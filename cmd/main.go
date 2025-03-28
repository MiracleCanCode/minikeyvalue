package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/minikeyvalue/src/config"
	"github.com/minikeyvalue/src/storage"
	"github.com/minikeyvalue/src/transport/tcp"
	"github.com/minikeyvalue/src/transport/tcp/handlers"
	"github.com/minikeyvalue/src/utils/aof"
	recoverdatafromaof "github.com/minikeyvalue/src/utils/recoverDataFromAof"
	"github.com/minikeyvalue/src/utils/retry"
	"github.com/minikeyvalue/src/utils/timeout"
	"go.uber.org/zap"
)

func main() {
	log, err := zap.NewDevelopment()
	if err != nil {
		fmt.Println(fmt.Errorf("main: failed create logger instance: %w", err))
		return
	}
	cfg := config.ParseCommandFlags()
	aofManager, err := aof.NewAOF(cfg.PathToStorageFile)
	if err != nil {
		log.Error("Failed create aof manager instance", zap.Error(err))
		return
	}
	storageInstance := storage.New(aofManager, false)
	recoveryStorage := storage.New(aofManager, true)
	recoverData := recoverdatafromaof.New(recoveryStorage)
	baseRetryDelayMiliseconds := 300
	retryAttempts := 5
	timeoutMillisecondsForRecoverData := 6000

	recoveryDataFn := func() error {
		if err := recoverData.Recover(cfg.PathToStorageFile); err != nil {
			return fmt.Errorf("RecoverData: failed recover data: %w", err)
		}
		return nil
	}
	if err := retry.RetryOperation(log, recoveryDataFn,
		baseRetryDelayMiliseconds, retryAttempts); err != nil {
		log.Error("Failed recover data", zap.Error(err))
		return
	}

	if err := timeout.Operation(timeoutMillisecondsForRecoverData,
		recoveryDataFn); err != nil {
		log.Error("Timeout for recover data operation!", zap.Error(err))
		return
	}

	var transport *tcp.TcpConn
	createTCPConnection := func() error {
		transport, err = tcp.NewWithConn(cfg.Port)
		if err != nil {
			return fmt.Errorf("CreateTcpConnectio: failed create tcp connection: %w", err)
		}
		return nil
	}
	if err := retry.RetryOperation(log, createTCPConnection, baseRetryDelayMiliseconds,
		retryAttempts); err != nil {
		log.Error("Failed create tcp connection", zap.Error(err))
		return
	}

	log.Info("IsaRedis start work", zap.String("port", cfg.Port))

	var wg sync.WaitGroup
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGINT, syscall.SIGBUS)

	go func() {
		<-quit
		log.Info("IsaRedis shutting down.....", zap.Time("time", time.Now()))
		transport.CloseConn()
		wg.Wait()
		log.Info("IsaRedis stop work", zap.Time("isa_redis_stopped_time", time.Now()))
	}()

	for {
		conn, err := transport.Conn.Accept()
		if err != nil {
			log.Error("Failed listen new connections", zap.Error(err))
			break
		}
		log.Info("Client connected!")
		handler := handlers.NewStorage(log, conn, storageInstance)
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.HandleClient()
		}()
	}
}
