package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gosnmp/gosnmp"
	log "github.com/sirupsen/logrus"
)

var version = "unknown"

// OIDs of interest.
// snmptranslate -m PowerNet-MIB -Pu -Tso | less
const (
	sPDUMasterConfigPDUName = ".1.3.6.1.4.1.318.1.1.4.3.3.0"
	sPDUOutletName          = ".1.3.6.1.4.1.318.1.1.4.5.2.1.3"
	sPDUOutletCtl           = ".1.3.6.1.4.1.318.1.1.4.4.2.1.3"
	sPDUIdentSerialNumber   = ".1.3.6.1.4.1.318.1.1.4.1.5.0"
	sPDUIdentModelNumber    = ".1.3.6.1.4.1.318.1.1.4.1.4.0"
)

type targetConfig struct {
	Host string
	Port uint16
}

type config struct {
	MQTT struct {
		Host string
		Port uint16
		User string
		Pass string
	}
	Targets []targetConfig
}

func parseConfig(path string) (*config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file: %w", err)
	}
	var conf config
	dec := toml.NewDecoder(file)
	if _, err := dec.Decode(&conf); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}

	return &conf, nil
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type Outlet struct {
	Name  string
	State bool
}

type PDUState struct {
	Name    string
	Serial  string
	Model   string
	Outlets []Outlet
}

type PDUCommand struct {
	Outlet int
	State  bool
}

func getPDUState(snmp *gosnmp.GoSNMP) (*PDUState, error) {
	res, err := snmp.Get([]string{sPDUMasterConfigPDUName, sPDUIdentSerialNumber, sPDUIdentModelNumber})
	if err != nil {
		return nil, fmt.Errorf("get PDU info: %w", err)
	}
	state := PDUState{
		Name:   string(res.Variables[0].Value.([]byte)),
		Serial: string(res.Variables[1].Value.([]byte)),
		Model:  string(res.Variables[2].Value.([]byte)),
	}

	outletNames, err := snmp.WalkAll(sPDUOutletName)
	if err != nil {
		return nil, fmt.Errorf("walk PDU outlet names: %w", err)
	}

	for _, val := range outletNames {
		state.Outlets = append(state.Outlets, Outlet{
			Name: string(val.Value.([]byte)),
		})
	}

	outletStates, err := snmp.WalkAll(sPDUOutletCtl)
	if err != nil {
		return nil, fmt.Errorf("walk PDU outlet states: %w", err)
	}
	for i, val := range outletStates {
		state.Outlets[i].State = val.Value.(int) == 1
	}
	return &state, nil
}

func runSNMP(host string, port uint16, stateCh chan PDUState, commandCh chan PDUCommand) {
	snmp := &gosnmp.GoSNMP{
		Target:    host,
		Port:      port,
		Timeout:   time.Duration(2) * time.Second,
		Transport: "udp",
		Community: "private",
		Version:   gosnmp.Version1,
		Retries:   3,
	}
	// Connect loop
connect_loop:
	for {
		log.Info("Opening SNMP connection")
		if err := snmp.Connect(); err != nil {
			log.Warnf("Error connecting SNMP target %s: `%s`. Sleeping...", host, err.Error())
			time.Sleep(time.Second * 5)
			continue // Try again
		}
		defer snmp.Conn.Close()
		poll := time.NewTicker(1 * time.Second)
		for {
			select {
			case <-poll.C:
				state, err := getPDUState(snmp)
				if err != nil {
					log.Errorf("getting pdu state: %s", err.Error())
					continue connect_loop // Reconnect
				}
				log.Debugf("Got state: %s", state)
				stateCh <- *state
			case cmd := <-commandCh:
				var value int
				if cmd.State {
					value = 1
				} else {
					value = 2
				}
				res, err := snmp.Set([]gosnmp.SnmpPDU{{
					Value: value,
					Name:  fmt.Sprintf("%s.%d", sPDUOutletCtl, cmd.Outlet),
					Type:  gosnmp.Integer,
				}})
				if err != nil {
					log.Errorf("setting pdu state: %s", err.Error())
				}
				if res.Error != gosnmp.NoError {
					log.Errorf("error in snmp set: %s", res.Error)
				}
			}
		}
	}
}

type HassDeviceConfig struct {
	Name         string `json:"name"`
	Identifiers  string `json:"identifiers"`
	Model        string `json:"model"`
	Manufacturer string `json:"manufacturer"`
}
type HassSwitchConfig struct {
	Name         string           `json:"name"`
	CommandTopic string           `json:"command_topic"`
	StateTopic   string           `json:"state_topic"`
	UniqueId     string           `json:"unique_id"`
	Device       HassDeviceConfig `json:"device"`
}

func spawnTarget(target targetConfig, mqttClient mqtt.Client) {
	stateCh := make(chan PDUState)
	commandCh := make(chan PDUCommand)
	var lastState PDUState

	go runSNMP(target.Host, target.Port, stateCh, commandCh)

	for state := range stateCh {
		for i, outlet := range state.Outlets {
			uid := fmt.Sprintf("apc_%s_%d", strings.ToLower(state.Serial), i)
			topicBase := fmt.Sprintf("homeassistant/switch/%s/", uid)
			if len(lastState.Outlets) == 0 || lastState.Outlets[i].Name != outlet.Name {
				// Update hass config
				hsc, err := json.Marshal(HassSwitchConfig{
					Name:         outlet.Name,
					CommandTopic: topicBase + "set",
					StateTopic:   topicBase + "state",
					UniqueId:     uid,
					Device: HassDeviceConfig{
						Name:         state.Name,
						Identifiers:  state.Serial,
						Model:        state.Model,
						Manufacturer: "APC",
					},
				})
				check(err)
				mqttClient.Publish(topicBase+"config", 0, false, hsc)
			}
			if len(lastState.Outlets) == 0 {
				// First time we've got a state, so subscribe to mqtt topic
				mqttClient.Subscribe(topicBase+"set", 0, func(idx int) func(mqtt.Client, mqtt.Message) {
					return func(_ mqtt.Client, message mqtt.Message) {
						commandCh <- PDUCommand{
							Outlet: idx,
							State:  string(message.Payload()) == "ON",
						}
					}
				}(i+1))
			}
			var oState string
			if outlet.State {
				oState = "ON"
			} else {
				oState = "OFF"
			}
			mqttClient.Publish(topicBase+"state", 0, false, oState)
		}
		lastState = state
	}
}
func main() {
	var configpath = flag.String("conf", "config.toml", "Path to toml config file")
	var verbose = flag.Bool("v", false, "Enable debug logging")
	flag.Parse()
	if *verbose {
		log.SetLevel(log.DebugLevel)
	}
	log.Infof("Starting version %s", version)
	conf, err := parseConfig(*configpath)
	check(err)

	mqttOpts := mqtt.NewClientOptions().AddBroker(
		fmt.Sprintf("tcp://%s:%d", conf.MQTT.Host, conf.MQTT.Port),
	).SetClientID("apc2mqtt").SetConnectTimeout(time.Second * 5)
	if conf.MQTT.User != "" {
		mqttOpts.SetUsername(conf.MQTT.User)
	}
	if conf.MQTT.Pass != "" {
		mqttOpts.SetPassword(conf.MQTT.Pass)
	}
	mqttClient := mqtt.NewClient(mqttOpts)
	for {
		if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
			log.Fatalf("connect mqtt: %s", token.Error())
		}

		for _, target := range conf.Targets {
			go spawnTarget(target, mqttClient)
		}
		select {}
	}
}
