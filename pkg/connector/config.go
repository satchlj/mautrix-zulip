package connector

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	up "go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	TopicNameTemplate string     `yaml:"topic_name_template"`
	InitialChatLimit  int        `yaml:"initial_chat_limit"`
	BridgeMuted       bool       `yaml:"bridge_muted"`
	Math              MathConfig `yaml:"math"`

	topicNameTemplate *template.Template `yaml:"-"`
}

func (zc *ZulipConnector) GetConfig() (example string, data any, upgrader up.Upgrader) {
	return ExampleConfig, &zc.Config, &up.StructUpgrader{
		SimpleUpgrader: up.SimpleUpgrader(upgradeConfig),
		Blocks:         [][]string{},
		Base:           ExampleConfig,
	}
}

type MathConfig struct {
	RenderImages bool   `yaml:"render_images"`
	Mode         string `yaml:"mode"`
	NodePath     string `yaml:"node_path"`
	RendererPath string `yaml:"renderer_path"`
	Foreground   string `yaml:"foreground"`
	Background   string `yaml:"background"`
	Halo         string `yaml:"halo"`
}

type umConfig Config

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	err := node.Decode((*umConfig)(c))
	if err != nil {
		return err
	}
	return c.PostProcess()
}

func (c *Config) PostProcess() (err error) {
	if c.TopicNameTemplate == "" {
		c.TopicNameTemplate = "#{{.Stream}} > {{.Topic}}"
	}
	switch c.Math.Mode {
	case "":
		c.Math.Mode = "inline"
	case "inline", "attachments":
	default:
		return fmt.Errorf("invalid math.mode %q", c.Math.Mode)
	}
	c.topicNameTemplate, err = template.New("topic_name").Parse(c.TopicNameTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse topic_name_template: %w", err)
	}
	return
}

type topicNameParams struct {
	Stream string
	Topic  string
}

func (c *Config) FormatTopicName(stream, topic string) string {
	var buf strings.Builder
	_ = c.topicNameTemplate.Execute(&buf, &topicNameParams{Stream: stream, Topic: topic})
	return buf.String()
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "topic_name_template")
	helper.Copy(up.Int, "initial_chat_limit")
	helper.Copy(up.Bool, "bridge_muted")
	helper.Copy(up.Bool, "math", "render_images")
	helper.Copy(up.Str, "math", "mode")
	helper.Copy(up.Str, "math", "node_path")
	helper.Copy(up.Str, "math", "renderer_path")
	helper.Copy(up.Str, "math", "foreground")
	helper.Copy(up.Str, "math", "background")
	helper.Copy(up.Str, "math", "halo")
}
