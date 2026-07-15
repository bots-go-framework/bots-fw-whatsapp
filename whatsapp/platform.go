// Package whatsapp adapts the WhatsApp Cloud API to the bots-fw framework.
//
// https://developers.facebook.com/documentation/business-messaging/whatsapp
package whatsapp

import (
	"github.com/bots-go-framework/bots-fw/botsfw"
	"github.com/bots-go-framework/bots-fw/botsfwconst"
)

// Platform is the bots platform descriptor for WhatsApp.
var Platform botsfw.BotPlatform = platform{}

// platform describes the WhatsApp platform.
type platform struct{}

// PlatformID is 'whatsapp'.
//
// Declared by bots-fw itself (botsfwconst.PlatformWhatsApp) before any WhatsApp
// code existed, so it is reused rather than redefined.
const PlatformID = botsfwconst.PlatformWhatsApp

// ID returns 'whatsapp'.
func (p platform) ID() string {
	return string(PlatformID)
}

// Version returns the Cloud API generation this adapter targets.
//
// Not a Graph API version: that is per-request and lives on the API client as
// wabotapi.DefaultGraphVersion.
func (p platform) Version() string {
	return "cloud-api"
}
