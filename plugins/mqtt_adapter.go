package plugins

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"

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
}

// NewMQTTAdapter creates a new MQTTAdapter given its configuration.
func NewMQTTAdapter(config MQTTAdapterConfig) Adapter {
	log.Printf("[MQTT Adapter] Creating new MQTT Adapter with config: %+v", config)
	return &MQTTAdapter{
		config: config,
	}
}

func (m *MQTTAdapter) Name() string {
	return "MQTT Adapter"
}

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

	// Optionally set username and password if provided.
	if m.config.Username != "" {
		opts.SetUsername(m.config.Username)
	}
	if m.config.Password != "" {
		opts.SetPassword(m.config.Password)
	}

	opts.SetDefaultPublishHandler(func(client mqtt.Client, msg mqtt.Message) {
		log.Printf("[MQTT Adapter] Received message on topic: %s", msg.Topic())
		var payload map[string]interface{}
		if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
			log.Printf("[MQTT Adapter] Error parsing JSON: %v", err)
			return
		}
		message, ok := payload["message"].(map[string]interface{})
		if !ok {
			log.Printf("[MQTT Adapter] Missing 'message' key")
			return
		}
		body, ok := message["body"].(map[string]interface{})
		if !ok {
			log.Printf("[MQTT Adapter] Missing 'body' key")
			return
		}
		transformed, ok := body["transformedPayload"].(map[string]interface{})
		if !ok {
			log.Printf("[MQTT Adapter] Missing 'transformedPayload' key")
			return
		}
		litersRaw, ok := transformed["liters"]
		if !ok {
			log.Printf("[MQTT Adapter] 'liters' not found")
			return
		}
		var liters int32
		switch v := litersRaw.(type) {
		case float64:
			liters = int32(v)
		case string:
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				log.Printf("[MQTT Adapter] Cannot parse liters: %v", err)
				return
			}
			liters = int32(f)
		default:
			log.Printf("[MQTT Adapter] Unexpected type for liters")
			return
		}
		attributes, ok := body["attributes"].(map[string]interface{})
		if !ok {
			log.Printf("[MQTT Adapter] Missing 'attributes' key")
			return
		}
		unitID, err := parseInt(attributes["unitID"])
		if err != nil {
			log.Printf("[MQTT Adapter] Invalid unitID: %v", err)
			return
		}
		modbusRegister, err := parseInt(attributes["modbusRegister"])
		if err != nil {
			log.Printf("[MQTT Adapter] Invalid modbusRegister: %v", err)
			return
		}
		log.Printf("[MQTT Adapter] Received liters: %d for unit %d at modbusRegister %d", liters, unitID, modbusRegister)
		dataCh <- Data{
			UnitID:         unitID,
			ModbusRegister: modbusRegister,
			Liters:         liters,
		}
	})
	opts.OnConnect = func(client mqtt.Client) {
		log.Printf("[MQTT Adapter] Connected to broker")
	}
	opts.OnConnectionLost = func(client mqtt.Client, err error) {
		log.Printf("[MQTT Adapter] Connection lost: %v", err)
	}
	m.client = mqtt.NewClient(opts)
	if token := m.client.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	}
	if token := m.client.Subscribe(m.config.Topic, 0, nil); token.Wait() && token.Error() != nil {
		return token.Error()
	}
	log.Printf("[MQTT Adapter] Subscribed to topic %s", m.config.Topic)
	go func() {
		// Block indefinitely.
		select {}
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
	tlsConfig := &tls.Config{
		RootCAs:      caCertPool,
		Certificates: []tls.Certificate{cert},
	}
	return tlsConfig, nil
}

func parseInt(v interface{}) (int, error) {
	switch val := v.(type) {
	case float64:
		return int(val), nil
	case string:
		return strconv.Atoi(val)
	default:
		return 0, fmt.Errorf("unexpected type")
	}
}
