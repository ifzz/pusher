/*
 * Copyright (c) 2016 TFG Co <backend@tfgco.com>
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

package extensions

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"

	"github.com/sideshow/apns2"
	token "github.com/sideshow/apns2/token"
	"github.com/topfreegames/pusher/structs"
)

// APNSPushQueue implements interfaces.APNSPushQueue
type APNSPushQueue struct {
	authKeyPath     string
	keyID           string
	teamID          string
	token           *token.Token
	pushChannel     chan *apns2.Notification
	responseChannel chan *structs.ResponseWithMetadata
	Logger          *log.Logger
	Config          *viper.Viper
	clients         chan *apns2.Client
	IsProduction    bool
	Closed          bool
}

// NewAPNSPushQueue returns a new instance of a APNSPushQueue
func NewAPNSPushQueue(
	authKeyPath, keyID,
	teamID string,
	isProduction bool,
	logger *log.Logger,
	config *viper.Viper,
) *APNSPushQueue {
	return &APNSPushQueue{
		authKeyPath:  authKeyPath,
		keyID:        keyID,
		teamID:       teamID,
		Logger:       logger,
		Config:       config,
		IsProduction: isProduction,
	}
}

// Configure configures queues and token
func (p *APNSPushQueue) Configure() error {
	l := p.Logger.WithField("method", "configure")
	err := p.configureCertificate()
	if err != nil {
		return err
	}
	p.Closed = false
	connectionPoolSize := p.Config.GetInt("apns.connectionPoolSize")
	p.clients = make(chan *apns2.Client, connectionPoolSize)
	for i := 0; i < connectionPoolSize; i++ {
		client := apns2.NewTokenClient(p.token)
		if p.IsProduction {
			client = client.Production()
		} else {
			client = client.Development()
		}
		p.clients <- client
	}
	l.Debug("clients configured")
	p.pushChannel = make(chan *apns2.Notification)
	p.responseChannel = make(chan *structs.ResponseWithMetadata)

	for i := 0; i < p.Config.GetInt("apns.concurrentWorkers"); i++ {
		go p.pushWorker()
	}
	return nil
}

func (p *APNSPushQueue) configureCertificate() error {
	l := p.Logger.WithField("method", "configureCertificate")
	authKey, err := token.AuthKeyFromFile(p.authKeyPath)
	if err != nil {
		l.WithError(err).Error("token error")
		return err
	}
	p.token = &token.Token{
		AuthKey: authKey,
		// KeyID from developer account (Certificates, Identifiers & Profiles -> Keys)
		KeyID: p.keyID,
		// TeamID from developer account (View Account -> Membership)
		TeamID: p.teamID,
	}
	l.Debug("token loaded")
	return nil
}

// ResponseChannel returns the response channel
func (p *APNSPushQueue) ResponseChannel() chan *structs.ResponseWithMetadata {
	return p.responseChannel
}

func (p *APNSPushQueue) pushWorker() {
	l := p.Logger.WithField("method", "pushWorker")

	for notification := range p.pushChannel {
		client := <-p.clients
		p.clients <- client

		res, err := client.Push(notification)
		if err != nil {
			l.WithError(err).Error("push error")
		}
		if res == nil {
			continue
		}
		newRes := &structs.ResponseWithMetadata{
			StatusCode:  res.StatusCode,
			Reason:      res.Reason,
			ApnsID:      res.ApnsID,
			Sent:        res.Sent(),
			DeviceToken: notification.DeviceToken,
		}
		p.responseChannel <- newRes

	}
}

// Push sends the notification
func (p *APNSPushQueue) Push(notification *apns2.Notification) {
	p.pushChannel <- notification
}

// Close close all the open channels
func (p *APNSPushQueue) Close() {
	close(p.pushChannel)
	close(p.responseChannel)
	p.Closed = true
}
