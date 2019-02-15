/*
 * Copyright (c) 2019 TFG Co <backend@tfgco.com>
 * Author: TFG Co <backend@tfgco.com>
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy of
 * this software and associated documentation files (the "Software"), to deal in
 * the Software without restriction, including without limitation the rights to
 * use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
 * the Software, and to permit persons to whom the Software is furnished to do so,
 * subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
 * FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
 * COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
 * IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
 * CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 */

package feedback

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	raven "github.com/getsentry/raven-go"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Listener will consume push feedbacks from a queue and use a broker to route
// the messages to a convenient handler
type Listener struct {
	Config                  *viper.Viper
	ConfigFile              string
	Logger                  *log.Logger
	Queue                   Queue
	Broker                  *Broker
	InvalidTokenHandler     *InvalidTokenHandler
	GracefulShutdownTimeout int
	run                     bool
	stopChannel             chan struct{}
}

// NewListener creates and return a new instance of feedback.Listener
func NewListener(configFile string, logger *log.Logger) (*Listener, error) {
	l := &Listener{
		ConfigFile:  configFile,
		Logger:      logger,
		stopChannel: make(chan struct{}),
	}
	err := l.configure()
	if err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Listener) loadConfigurationDefaults() {
	l.Config.SetDefault("feedbackListeners.gracefulShutdownTimeout", 1)
}

func (l *Listener) configure() error {
	l.Config = viper.New()
	l.Config.SetConfigFile(l.ConfigFile)
	l.Config.SetEnvPrefix("pusher")
	l.Config.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	l.Config.AutomaticEnv()
	err := l.Config.ReadInConfig()
	if err != nil {
		return err
	}

	l.loadConfigurationDefaults()
	l.configureSentry()

	l.GracefulShutdownTimeout = l.Config.GetInt("feedbackListeners.gracefulShutdownTimeout")
	q, err := NewKafkaConsumer(
		l.Config, l.Logger,
		&l.stopChannel, nil,
	)
	if err != nil {
		return errors.Errorf("error creating new queue: %s", err.Error())
	}
	l.Queue = q

	broker, err := NewBroker(l.Logger, l.Config, q.MessagesChannel(), l.Queue.PendingMessagesWaitGroup())
	if err != nil {
		return errors.Errorf("error creating new broke: %s", err.Error())
	}
	l.Broker = broker

	handler, err := NewInvalidTokenHandler(l.Logger, l.Config, &l.Broker.InvalidTokenOutChan)
	if err != nil {
		return errors.Errorf("error creating new invalid token handler: %s", err.Error())
	}
	l.InvalidTokenHandler = handler

	return nil
}

func (l *Listener) configureSentry() {
	ll := l.Logger.WithFields(log.Fields{
		"source":    "feedbackListener",
		"operation": "configureSentry",
	})

	sentryURL := l.Config.GetString("sentry.url")
	if sentryURL != "" {
		raven.SetDSN(sentryURL)
	}

	ll.Info("Configured sentry successfully.")
}

// Start starts the listener
func (l *Listener) Start() {
	l.run = true
	log := l.Logger.WithField(
		"method", "start",
	)
	log.Info("starting the feedback listener...")

	go l.Queue.ConsumeLoop()
	l.Broker.Start()
	l.InvalidTokenHandler.Start()

	sigchan := make(chan os.Signal)
	signal.Notify(sigchan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	for l.run == true {
		select {
		case sig := <-sigchan:
			log.WithField("signal", sig.String()).Warn("terminating due to caught signal")
			l.run = false
		case <-l.stopChannel:
			fmt.Println("STOPPING CHANNEL")
			log.Warn("Stop channel closed\n")
			l.run = false
		}
	}

	l.Stop()
	// l.Queue.StopConsuming()
	// l.gracefulShutdown(l.Queue.PendingMessagesWaitGroup(), time.Duration(l.GracefulShutdownTimeout)*time.Second)
}

func (l *Listener) Stop() {
	l.run = false
	l.Queue.Cleanup()
	l.gracefulShutdown(l.Queue.PendingMessagesWaitGroup(), time.Duration(l.GracefulShutdownTimeout)*time.Second)
}

// GracefulShutdown waits for wg do complete then exits
func (l *Listener) gracefulShutdown(wg *sync.WaitGroup, timeout time.Duration) {
	log := l.Logger.WithFields(log.Fields{
		"method":  "gracefulShutdown",
		"timeout": int(timeout.Seconds()),
	})

	if wg != nil {
		log.Info("listener is waiting to exit...")
		e := WaitTimeout(wg, timeout)
		if e {
			fmt.Println("timeout")
			log.Warn("exited listener because of graceful shutdown timeout")
		} else {
			fmt.Println("gracefully")
			log.Info("exited listener gracefully")
		}
	}
}

// WaitTimeout waits for the waitgroup for the specified max timeout.
// Returns true if waiting timed out.
// got from http://stackoverflow.com/a/32843750/3987733
func WaitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false // completed normally
	case <-time.After(timeout):
		return true // timed out
	}
}