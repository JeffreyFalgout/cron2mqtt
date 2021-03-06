package hass

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kballard/go-shellquote"

	"github.com/JeffreyFalgout/cron2mqtt/cron"
	"github.com/JeffreyFalgout/cron2mqtt/mqtt"
	"github.com/JeffreyFalgout/cron2mqtt/mqtt/mqttcron"
)

const (
	failureState = "failure"
	successState = "success"
)

var (
	now = time.Now
)

// Plugin provides home assistant specific funcionality to mqttcron.CronJob.
type Plugin struct {
	mqttcron.NopPlugin
	discoveryPrefix string

	problemConfigTopic  string
	durationConfigTopic string
}

func NewPlugin() mqttcron.Plugin {
	return &Plugin{
		discoveryPrefix: "homeassistant", // TODO: Make this configurable.
	}
}

func (p *Plugin) Init(cj *mqttcron.CronJob, reg mqttcron.TopicRegister) error {
	d, err := mqttcron.CurrentDevice()
	if err != nil {
		return err
	}
	nodeID, err := nodeID(d)
	if err != nil {
		return err
	}

	// TODO: Add more sensors.
	//   - stdout/stderr size?
	p.problemConfigTopic = fmt.Sprintf("%s/binary_sensor/%s/%s/config", p.discoveryPrefix, nodeID, cj.ID())
	p.durationConfigTopic = fmt.Sprintf("%s/sensor/%s/%s_duration/config", p.discoveryPrefix, nodeID, cj.ID())
	reg.RegisterTopic(p.problemConfigTopic, mqtt.Retain)
	reg.RegisterTopic(p.durationConfigTopic, mqtt.Retain)
	return nil
}

func (p *Plugin) OnCreate(cj *mqttcron.CronJob, pub mqttcron.Publisher) error {
	d, err := mqttcron.CurrentDevice()
	if err != nil {
		return err
	}
	var cp *mqttcron.CorePlugin
	if !cj.Plugin(&cp) {
		return fmt.Errorf("could not retrieve mqttcron.CorePlugin")
	}

	dev := deviceConfig{
		Name:        d.Hostname,
		Identifiers: []string{d.ID},
	}
	name := fmt.Sprintf("[%s@%s] %s", d.User.Username, d.Hostname, commandName(cj.ID(), cj.Command))
	problemConf := binarySensor{
		common: common{
			BaseTopic:       cp.ResultsTopic,
			StateTopic:      "~",
			ValueTemplate:   fmt.Sprintf("{%% if value_json.%s == 0 %%}%s{%% else %%}%s{%% endif %%}", mqttcron.ExitCodeAttributeName, successState, failureState),
			AttributesTopic: "~",

			Device:   dev,
			UniqueID: cj.ID(),
			ObjectID: "cron_job_" + cj.ID(),
			Name:     name,

			Icon: "mdi:robot",
		},

		DeviceClass: binarySensorDeviceClasses.problem,
		// These are inverted on purpose thanks to "device_class": "problem"
		PayloadOn:  failureState,
		PayloadOff: successState,
	}
	durationConf := sensor{
		common: common{
			BaseTopic:       cp.ResultsTopic,
			StateTopic:      "~",
			ValueTemplate:   fmt.Sprintf("{{value_json.%s}}", mqttcron.DurationAttributeName),
			AttributesTopic: "~",

			Device:   dev,
			UniqueID: cj.ID() + "_duration",
			ObjectID: fmt.Sprintf("cron_job_%s_duration", cj.ID()),
			Name:     "duration of " + name,

			Icon: "mdi:timer",
		},

		DeviceClass:       sensorDeviceClasses.duration,
		UnitOfMeasurement: units.milliseconds,
		StateClass:        stateClasses.measurement,
	}
	if cj.Schedule != nil {
		exp := seconds(expireAfter(cj.Schedule))
		problemConf.ExpireAfter = &exp
		durationConf.ExpireAfter = &exp
	}
	pc, err := json.Marshal(problemConf)
	if err != nil {
		return fmt.Errorf("could not marshal discovery config: %w", err)
	}
	dc, err := json.Marshal(durationConf)
	if err != nil {
		return fmt.Errorf("could not marshal discovery config: %w", err)
	}
	return mqttcron.MultiPublish(
		func() error { return pub.Publish(p.problemConfigTopic, mqtt.QoSExactlyOnce, mqtt.Retain, pc) },
		func() error { return pub.Publish(p.durationConfigTopic, mqtt.QoSExactlyOnce, mqtt.Retain, dc) })
}

func nodeID(d mqttcron.Device) (string, error) {
	id := fmt.Sprintf("cron2mqtt_%s_%s", d.ID, d.User.Uid)
	if err := mqttcron.ValidateTopicComponent(id); err != nil {
		return "", fmt.Errorf("calculated node ID is invalid: %w", err)
	}
	return id, nil
}

func commandName(id string, c *cron.Command) string {
	if c == nil {
		return id
	}
	args, ok := c.Args()
	if !ok {
		return c.String()
	}

	var shArgs []string
	for i, arg := range args {
		if arg == "--" {
			shArgs = args[i+1:]
			break
		}
		if arg == id && i < len(args)-1 {
			shArgs = []string{}
			continue
		}
		if shArgs == nil || strings.HasPrefix(arg, "-") {
			continue
		}
		shArgs = append(shArgs, arg)
	}
	if len(shArgs) > 0 {
		if sp, err := shellquote.Split(strings.Join(shArgs, " ")); err == nil {
			return strings.Join(sp, " ")
		}
	}
	return strings.Join(args, " ")
}

func expireAfter(s *cron.Schedule) time.Duration {
	now := now()
	next := s.Next(now)
	secondNext := s.Next(next)
	gap := secondNext.Sub(next)

	exp1 := next.Add(60 * time.Second)
	exp2 := next.Add(time.Duration(int(gap.Nanoseconds()) / 2))
	var dur time.Duration
	if exp1.Before(exp2) {
		dur = exp1.Sub(now)
	} else {
		dur = exp2.Sub(now)
	}
	return dur
}
