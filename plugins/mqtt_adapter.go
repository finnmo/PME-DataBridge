package plugins

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTTAdapterConfig holds configuration settings for the MQTT adapter.
type MQTTAdapterConfig struct {
	Broker   string `mapstructure:"broker"`
	Port     int    `mapstructure:"port"`
	Topic    string `mapstructure:"topic"`
	CA       string `mapstructure:"ca"`
	Cert     string `mapstructure:"cert"`
	Key      string `mapstructure:"key"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

// MQTTAdapter implements the Adapter interface.
type MQTTAdapter struct {
	config MQTTAdapterConfig
	client mqtt.Client
	// Add message buffer for reliability
	messageBuffer []Data
	bufferMutex   sync.RWMutex
	bufferSize    int
}

// NewMQTTAdapter creates a new MQTTAdapter given its configuration.
func NewMQTTAdapter(config MQTTAdapterConfig) Adapter {
	return &MQTTAdapter{
		config:        config,
		messageBuffer: make([]Data, 0, 100), // Buffer up to 100 messages
		bufferSize:    100,
	}
}

func (m *MQTTAdapter) Name() string {
	return "MQTT Adapter"
}

// Start connects to the MQTT broker, subscribes to the topic, and processes messages.
func (m *MQTTAdapter) Start(dataCh chan<- Data) error {
	tlsConfig, err := newTLSConfig(m.config.CA, m.config.Cert, m.config.Key)
	if err != nil {
		return err
	}

	opts := mqtt.NewClientOptions()
	brokerURL := fmt.Sprintf("%s:%d", m.config.Broker, m.config.Port)
	opts.AddBroker(brokerURL)
	opts.SetClientID("PME-DataBridge-mqtt")
	opts.SetTLSConfig(tlsConfig)

	// Add connection reliability settings
	opts.SetKeepAlive(30 * time.Second)
	opts.SetPingTimeout(10 * time.Second)
	opts.SetConnectTimeout(30 * time.Second)
	opts.SetMaxReconnectInterval(1 * time.Minute)
	opts.SetAutoReconnect(true)
	opts.SetCleanSession(false) // Keep session to avoid losing messages

	if m.config.Username != "" {
		opts.SetUsername(m.config.Username)
	}
	if m.config.Password != "" {
		opts.SetPassword(m.config.Password)
	}

	// This publish handler is called for each incoming message on the subscribed topic(s).
	opts.SetDefaultPublishHandler(func(client mqtt.Client, msg mqtt.Message) {
		log.Printf("[MQTT Adapter] Received message on topic %s: %s", msg.Topic(), string(msg.Payload()))

		var payload map[string]interface{}
		if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
			log.Printf("[MQTT Adapter] JSON parse error: %v", err)
			return
		}

		// Attempt to parse the relevant fields:
		message, _ := payload["message"].(map[string]interface{})
		body, _ := message["body"].(map[string]interface{})
		transformed, _ := body["transformedPayload"].(map[string]interface{})
		litersRaw, _ := transformed["liters"]
		attributes, _ := body["attributes"].(map[string]interface{})

		log.Printf("[MQTT Adapter] Parsed data - liters: %v, attributes: %+v", litersRaw, attributes)

		var liters int32
		switch v := litersRaw.(type) {
		case float64:
			liters = int32(v)
		case string:
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				log.Printf("[MQTT Adapter] Cannot parse liters from string: %v", err)
				return
			}
			liters = int32(f)
		default:
			log.Printf("[MQTT Adapter] Unexpected liters type: %T", litersRaw)
			return
		}
		unitID, errU := parseInt(attributes["unitID"])
		modbusRegister, errR := parseInt(attributes["modbusRegister"])
		if errU != nil || errR != nil {
			log.Printf("[MQTT Adapter] Missing or invalid unitID/modbusRegister in message. unitID: %v, modbusRegister: %v",
				attributes["unitID"], attributes["modbusRegister"])
			return
		}

		// Create normalized data
		data := Data{
			UnitID:         unitID,
			ModbusRegister: modbusRegister,
			Liters:         liters,
		}

		// Try to send to channel, buffer if full
		select {
		case dataCh <- data:
			log.Printf("[MQTT->DataCh] Sending data - liters=%d, unitID=%d, reg=%d", liters, unitID, modbusRegister)
		default:
			// Channel is full, buffer the message
			m.bufferMutex.Lock()
			if len(m.messageBuffer) < m.bufferSize {
				m.messageBuffer = append(m.messageBuffer, data)
				log.Printf("[MQTT Adapter] Buffered message - buffer size: %d", len(m.messageBuffer))
			} else {
				log.Printf("[MQTT Adapter] Buffer full, dropping oldest message")
				// Remove oldest message and add new one
				m.messageBuffer = append(m.messageBuffer[1:], data)
			}
			m.bufferMutex.Unlock()
		}
	})

	opts.OnConnect = func(client mqtt.Client) {
		log.Printf("[MQTT Adapter] Connected to %s", brokerURL)
	}
	opts.OnConnectionLost = func(client mqtt.Client, err error) {
		log.Printf("[MQTT Adapter] Connection lost: %v", err)
		// Attempt to reconnect
		go func() {
			time.Sleep(5 * time.Second)
			if token := client.Connect(); token.Wait() && token.Error() != nil {
				log.Printf("[MQTT Adapter] Failed to reconnect: %v", token.Error())
			}
		}()
	}

	// Add connection monitoring
	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		log.Printf("[MQTT Adapter] Connection lost: %v", err)
	})
	opts.SetReconnectingHandler(func(client mqtt.Client, opts *mqtt.ClientOptions) {
		log.Printf("[MQTT Adapter] Attempting to reconnect...")
	})
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		log.Printf("[MQTT Adapter] Successfully reconnected")
	})

	m.client = mqtt.NewClient(opts)
	if token := m.client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("failed to connect to MQTT broker: %w", token.Error())
	}

	// Subscribe with QoS 1 for better reliability
	if token := m.client.Subscribe(m.config.Topic, 1, nil); token.Wait() && token.Error() != nil {
		return fmt.Errorf("failed to subscribe to topic: %w", token.Error())
	}
	log.Printf("[MQTT Adapter] Subscribed to topic '%s' on broker '%s'", m.config.Topic, brokerURL)

	// Start a health check goroutine
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if !m.client.IsConnected() {
				log.Printf("[MQTT Adapter] Health check: Not connected, attempting to reconnect...")
				if token := m.client.Connect(); token.Wait() && token.Error() != nil {
					log.Printf("[MQTT Adapter] Health check: Reconnection failed: %v", token.Error())
				}
			} else {
				log.Printf("[MQTT Adapter] Health check: Connection is healthy")

				// Try to flush buffered messages
				m.bufferMutex.Lock()
				if len(m.messageBuffer) > 0 {
					log.Printf("[MQTT Adapter] Attempting to flush %d buffered messages", len(m.messageBuffer))
					for i, data := range m.messageBuffer {
						select {
						case dataCh <- data:
							log.Printf("[MQTT Adapter] Flushed buffered message %d", i+1)
						default:
							// Still can't send, keep remaining messages
							m.messageBuffer = m.messageBuffer[i:]
							log.Printf("[MQTT Adapter] Could not flush all messages, %d remaining", len(m.messageBuffer))
							break
						}
					}
					// Clear successfully sent messages
					if len(m.messageBuffer) == 0 {
						m.messageBuffer = m.messageBuffer[:0]
					}
				}
				m.bufferMutex.Unlock()
			}
		}
	}()

	return nil
}

func newTLSConfig(caFile, certFile, keyFile string) (*tls.Config, error) {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		RootCAs:      caCertPool,
		Certificates: []tls.Certificate{cert},
	}, nil
}

func parseInt(v interface{}) (int, error) {
	switch val := v.(type) {
	case float64:
		return int(val), nil
	case string:
		return strconv.Atoi(val)
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}

// IsConnected returns true if the MQTT client is connected
func (m *MQTTAdapter) IsConnected() bool {
	return m.client != nil && m.client.IsConnected()
}

// Reconnect attempts to reconnect the MQTT client
func (m *MQTTAdapter) Reconnect() error {
	if m.client == nil {
		return fmt.Errorf("client not initialized")
	}

	log.Printf("[MQTT Adapter] Manual reconnection attempt...")
	if token := m.client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("manual reconnection failed: %w", token.Error())
	}

	// Resubscribe to topic
	if token := m.client.Subscribe(m.config.Topic, 1, nil); token.Wait() && token.Error() != nil {
		return fmt.Errorf("failed to resubscribe: %w", token.Error())
	}

	log.Printf("[MQTT Adapter] Manual reconnection successful")
	return nil
}

// GetBufferedMessages returns a copy of buffered messages and clears the buffer
func (m *MQTTAdapter) GetBufferedMessages() []Data {
	m.bufferMutex.Lock()
	defer m.bufferMutex.Unlock()

	if len(m.messageBuffer) == 0 {
		return nil
	}

	messages := make([]Data, len(m.messageBuffer))
	copy(messages, m.messageBuffer)
	m.messageBuffer = m.messageBuffer[:0] // Clear buffer
	return messages
}

// Disconnect gracefully disconnects the MQTT client
func (m *MQTTAdapter) Disconnect() {
	if m.client != nil {
		m.client.Disconnect(250)
	}
}
