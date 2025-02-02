package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/safing/portmaster/base/info"
	"github.com/safing/portmaster/base/log"
	"github.com/safing/portmaster/base/metrics"
	"github.com/safing/portmaster/service/mgr"
	"github.com/safing/portmaster/service/updates"
	"github.com/safing/portmaster/service/updates/helper"
	"github.com/safing/portmaster/spn"
	"github.com/safing/portmaster/spn/conf"
)

func init() {
	flag.BoolVar(&updates.RebootOnRestart, "reboot-on-restart", false, "reboot server on auto-upgrade")
}

var sigUSR1 = syscall.Signal(0xa)

func main() {
	flag.Parse()

	// Set name and license.
	info.Set("SPN Hub", "0.7.8", "GPLv3")

	// Configure metrics.
	_ = metrics.SetNamespace("hub")

	// Configure user agent and updates.
	updates.UserAgent = fmt.Sprintf("SPN Hub (%s %s)", runtime.GOOS, runtime.GOARCH)
	helper.IntelOnly()

	// Set SPN public hub mode.
	conf.EnablePublicHub(true)

	// Create instance.
	var execCmdLine bool
	instance, err := spn.New()
	switch {
	case err == nil:
		// Continue
	case errors.Is(err, mgr.ErrExecuteCmdLineOp):
		execCmdLine = true
	default:
		fmt.Printf("error creating an instance: %s\n", err)
		os.Exit(2)
	}

	// Execute command line operation, if requested or available.
	switch {
	case !execCmdLine:
		// Run service.
	case instance.CommandLineOperation == nil:
		fmt.Println("command line operation execution requested, but not set")
		os.Exit(3)
	default:
		// Run the function and exit.
		err = instance.CommandLineOperation()
		if err != nil {
			fmt.Fprintf(os.Stderr, "command line operation failed: %s\n", err)
			os.Exit(3)
		}
		os.Exit(0)
	}

	// Set default log level.
	log.SetLogLevel(log.WarningLevel)
	_ = log.Start()

	// Start
	go func() {
		err = instance.Start()
		if err != nil {
			fmt.Printf("instance start failed: %s\n", err)
			os.Exit(1)
		}
	}()

	// Wait for signal.
	signalCh := make(chan os.Signal, 1)
	signal.Notify(
		signalCh,
		os.Interrupt,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
		sigUSR1,
	)

	select {
	case sig := <-signalCh:
		// Only print and continue to wait if SIGUSR1
		if sig == sigUSR1 {
			printStackTo(os.Stderr, "PRINTING STACK ON REQUEST")
		} else {
			fmt.Println(" <INTERRUPT>") // CLI output.
			slog.Warn("program was interrupted, stopping")
		}

	case <-instance.Stopped():
		log.Shutdown()
		os.Exit(instance.ExitCode())
	}

	// Catch signals during shutdown.
	// Rapid unplanned disassembly after 5 interrupts.
	go func() {
		forceCnt := 5
		for {
			<-signalCh
			forceCnt--
			if forceCnt > 0 {
				fmt.Printf(" <INTERRUPT> again, but already shutting down - %d more to force\n", forceCnt)
			} else {
				printStackTo(os.Stderr, "PRINTING STACK ON FORCED EXIT")
				os.Exit(1)
			}
		}
	}()

	// Rapid unplanned disassembly after 3 minutes.
	go func() {
		time.Sleep(3 * time.Minute)
		printStackTo(os.Stderr, "PRINTING STACK - TAKING TOO LONG FOR SHUTDOWN")
		os.Exit(1)
	}()

	// Stop instance.
	if err := instance.Stop(); err != nil {
		slog.Error("failed to stop", "err", err)
	}
	log.Shutdown()
	os.Exit(instance.ExitCode())
}

func printStackTo(writer io.Writer, msg string) {
	_, err := fmt.Fprintf(writer, "===== %s =====\n", msg)
	if err == nil {
		err = pprof.Lookup("goroutine").WriteTo(writer, 1)
	}
	if err != nil {
		slog.Error("failed to write stack trace", "err", err)
	}
}
