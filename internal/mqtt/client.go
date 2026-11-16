// Package mqtt wraps the Paho MQTT client with the single outbound connection,
// auto-reconnect, credentials and Last-Will behaviour TrailTransfer needs.
package mqtt

import (
	"errors"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// Options configures the client.
type Options struct {
	Broker     string
	ClientID   string
	Username   string
	Password   string
	KeepAlive  time.Duration
	LWTTopic   string
	LWTPayload []byte
}

// Client is a thin wrapper exposing just the publish/subscribe surface the
// worker uses.
type Client struct {
	c         paho.Client
	onConnect func(*Client)
}

// New builds an (unconnected) client. Call OnConnect before Connect to register
// a handler that runs on every successful (re)connect.
func New(o Options) *Client {
	cl := &Client{}
	opts := paho.NewClientOptions().
		AddBroker(o.Broker).
		SetClientID(o.ClientID).
		SetKeepAlive(o.KeepAlive).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(60 * time.Second)

	if o.Username != "" {
		opts.SetUsername(o.Username)
		opts.SetPassword(o.Password)
	}
	if o.LWTTopic != "" {
		opts.SetBinaryWill(o.LWTTopic, o.LWTPayload, 1, true)
	}
	opts.SetOnConnectHandler(func(paho.Client) {
		if cl.onConnect != nil {
			cl.onConnect(cl)
		}
	})

	cl.c = paho.NewClient(opts)
	return cl
}

// OnConnect registers a handler invoked on each successful (re)connect, e.g. to
// (re)subscribe and republish retained health/capabilities.
func (c *Client) OnConnect(f func(*Client)) { c.onConnect = f }

// Connect blocks until the first connection succeeds or errors.
func (c *Client) Connect() error {
	tok := c.c.Connect()
	tok.Wait()
	return tok.Error()
}

// publishTimeout bounds the wait for a broker confirmation. A job must not
// hang on a status message because the broker is away.
const publishTimeout = 10 * time.Second

// ErrNotConnected reports that there is no broker connection right now.
var ErrNotConnected = errors.New("mqtt: not connected to the broker")

// ErrNotConfirmed reports that the broker did not confirm a publish in time.
// The client may still deliver the message after a reconnect.
var ErrNotConfirmed = errors.New("mqtt: publish not confirmed by the broker in time")

// Publish sends a message and waits up to publishTimeout for the broker's
// confirmation (QoS 1) or the handoff (QoS 0).
func (c *Client) Publish(topic string, qos byte, retain bool, payload []byte) error {
	if !c.c.IsConnectionOpen() {
		// Nothing is queued for later here: sealed records are published
		// from the journal by the outbox, and status, progress and log
		// messages describe a moment that has passed by the time the
		// broker is back.
		return ErrNotConnected
	}
	tok := c.c.Publish(topic, qos, retain, payload)
	if !tok.WaitTimeout(publishTimeout) {
		return ErrNotConfirmed
	}
	return tok.Error()
}

// IsConnected reports whether the client has a broker connection right now.
func (c *Client) IsConnected() bool { return c.c.IsConnectionOpen() }

// Subscribe registers a callback for a topic filter.
func (c *Client) Subscribe(topic string, qos byte, cb func(topic string, payload []byte)) error {
	tok := c.c.Subscribe(topic, qos, func(_ paho.Client, m paho.Message) {
		cb(m.Topic(), m.Payload())
	})
	tok.Wait()
	return tok.Error()
}

// Disconnect publishes nothing and closes the connection, waiting up to 250ms.
func (c *Client) Disconnect() { c.c.Disconnect(250) }
