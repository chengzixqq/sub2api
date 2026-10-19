package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionBindingModes(t *testing.T) {
	login := &SessionBinding{IP: "198.51.100.1", UserAgent: "Sub2Mobile-iOS", DeviceID: "6F1C2A7E"}

	strict := login.HashFor(SessionBindingModeStrict)
	require.Equal(t, login.Hash(), strict, "strict keeps the legacy unprefixed IP + UA hash")
	device := login.HashFor(SessionBindingModeDevice)
	require.Contains(t, device, bindingPrefixDevice)
	uaOnly := login.HashFor(SessionBindingModeUAOnly)
	require.Contains(t, uaOnly, bindingPrefixUA)
	off := login.HashFor(SessionBindingModeOff)

	newIP := &SessionBinding{IP: "203.0.113.9", UserAgent: "Sub2Mobile-iOS", DeviceID: "6F1C2A7E"}
	require.False(t, newIP.Matches(strict), "strict fails on an IP change")
	require.True(t, newIP.Matches(device), "device mode survives network changes")
	require.True(t, newIP.Matches(uaOnly))
	require.True(t, newIP.Matches(off))

	otherDevice := &SessionBinding{IP: "198.51.100.1", UserAgent: "Sub2Mobile-iOS", DeviceID: "OTHER"}
	require.False(t, otherDevice.Matches(device))
	noDevice := &SessionBinding{IP: "198.51.100.1", UserAgent: "Sub2Mobile-iOS"}
	require.False(t, noDevice.Matches(device), "a missing device id does not satisfy device mode")

	otherUA := &SessionBinding{IP: "198.51.100.1", UserAgent: "curl/8", DeviceID: "6F1C2A7E"}
	require.False(t, otherUA.Matches(uaOnly))
	require.False(t, otherUA.Matches(strict))

	require.True(t, newIP.Matches(""), "tokens issued before binding existed pass")
	require.Equal(t, "", (&SessionBinding{UserAgent: "x"}).HashFor(SessionBindingModeDevice))
}

func TestSessionBindingModeIsInheritedNotDowngraded(t *testing.T) {
	// A Web (strict) token keeps strict on refresh even if the request claims a native client.
	require.Equal(t, SessionBindingModeStrict, bindingModeOf("abcdef"))
	require.Equal(t, SessionBindingModeDevice, bindingModeOf(bindingPrefixDevice+"x"))
	require.Equal(t, SessionBindingModeUAOnly, bindingModeOf(bindingPrefixUA+"x"))
	require.Equal(t, SessionBindingModeOff, bindingModeOf(bindingPrefixOff))
}

func TestValidateSessionBindingNativeMode(t *testing.T) {
	for in, want := range map[string]string{"": "strict", "strict": "strict", "device": "device", "ua_only": "ua_only", "off": "off"} {
		got, err := ValidateSessionBindingNativeMode(in)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := ValidateSessionBindingNativeMode("loose")
	require.Error(t, err)
}

func TestSessionClientIsNative(t *testing.T) {
	require.True(t, (&SessionClient{Type: "ios"}).IsNative())
	require.True(t, (&SessionClient{Type: "Android"}).IsNative())
	require.False(t, (&SessionClient{Type: "web"}).IsNative())
	var nilClient *SessionClient
	require.False(t, nilClient.IsNative())
}
