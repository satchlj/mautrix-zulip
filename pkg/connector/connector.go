package connector

import (
	"context"

	"maunium.net/go/mautrix/bridgev2"
)

type ZulipConnector struct {
	Bridge *bridgev2.Bridge
	Config Config

	math *mathImageRenderer
}

var _ bridgev2.NetworkConnector = (*ZulipConnector)(nil)

func (zc *ZulipConnector) Init(bridge *bridgev2.Bridge) {
	zc.Bridge = bridge
}

func (zc *ZulipConnector) Start(ctx context.Context) error {
	if zc.Config.Math.RenderImages {
		zc.math = newMathImageRenderer(&zc.Config.Math, zc.Bridge.Log)
	}
	return nil
}

func (zc *ZulipConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "Zulip",
		NetworkURL:           "https://zulip.org/",
		NetworkIcon:          "mxc://maunium.net/pKfjZaIjOREmSJoyYgnmonJZ",
		NetworkID:            "zulip",
		BeeperBridgeType:     "zulip",
		DefaultPort:          29342,
		DefaultCommandPrefix: "!zulip",
	}
}
