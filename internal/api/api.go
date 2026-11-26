// Package api holds Pentair Home cloud endpoint constants, region
// profiles and wire types. All reverse-engineered constants live here.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/emergingrobotics/gopentair/internal/auth"
)

// Hosts.
const (
	RESTBase = "https://api.pentair.cloud"
	WSURL    = "wss://g44t970cbi.execute-api.us-west-2.amazonaws.com/prod"
	// UnauthAPIKey is the app's API key for pre-login calls (detectUser).
	UnauthAPIKey = "p4u60la7xH7q4aat3Tzrq9BfspTOslIs6oXzIhuS"
)

// Paths.
const (
	PathDetectUser    = "/user2/user2-service/unauth/detectUser"
	PathProfile       = "/user/user-service/common/profile"
	PathProfileV2     = "/user/user-service/common/profilev2"
	PathDevicesV1     = "/device/device-service/user/devices"
	PathListDevicesV2 = "/device2/device2-service/user/listdevices"
	PathDeviceV1      = "/device/device-service/user/device/" // + id (GET state, PUT fields)
	PathDeviceStates  = "/device2/device2-service/user/device"
)

// SetDeviceSuccess is the ack code for field writes.
const SetDeviceSuccess = "set_device_success"

// Regions.
var (
	RegionUS = auth.Region{
		Name:           "us",
		AWSRegion:      "us-west-2",
		UserPoolID:     "us-west-2_lbiduhSwD",
		ClientID:       "3de110o697faq7avdchtf07h4v",
		IdentityPoolID: "us-west-2:6f950f85-af44-43d9-b690-a431f753e9aa",
	}
	// RegionEU is experimental: the pool ID comes from a decompile; the
	// client and identity pool IDs are unknown and must be supplied.
	RegionEU = auth.Region{
		Name:       "eu",
		AWSRegion:  "eu-west-1",
		UserPoolID: "eu-west-1_rsBGJFuRd",
	}
)

// WireField is one entry of a device's "fields" map.
type WireField struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
	Min   json.RawMessage `json:"min"`
	Max   json.RawMessage `json:"max"`
}

// RawString renders a JSON scalar as the string the API means: strings
// unquoted, numbers/bools verbatim, null as "".
func RawString(r json.RawMessage) string {
	r = bytes.TrimSpace(r)
	if len(r) == 0 || string(r) == "null" {
		return ""
	}
	if r[0] == '"' {
		var s string
		if err := json.Unmarshal(r, &s); err == nil {
			return s
		}
	}
	return string(r)
}

// ProductInfo is the listing's productInfo object.
type ProductInfo struct {
	NickName string `json:"nickName"`
	Model    string `json:"model"`
	Maker    string `json:"maker"`
	Visible  *bool  `json:"visible"`
}

// WireDevice covers both listing and state records.
type WireDevice struct {
	DeviceID     string               `json:"deviceId"`
	DeviceType   string               `json:"deviceType"`
	Status       string               `json:"status"`
	Pname        string               `json:"pname"`
	ARN          string               `json:"arn"`
	ProductInfo  ProductInfo          `json:"productInfo"`
	Devices      map[string]any       `json:"devices"`
	Online       *bool                `json:"online"`
	Alarm        *bool                `json:"alarm"`
	FwVersion    json.RawMessage      `json:"fwVersion"`
	Fields       map[string]WireField `json:"fields"`
	ReportedDate int64                `json:"reportedDate"`
	Timestamp    json.RawMessage      `json:"timestamp"`
}

// DecodeDevices decodes a device listing from either the v1
// ({"data":[...]}) or v2 ({"response":[...]}) shape. It returns each
// record alongside its raw JSON.
func DecodeDevices(body []byte) ([]WireDevice, []json.RawMessage, error) {
	var env struct {
		Data     json.RawMessage `json:"data"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, nil, fmt.Errorf("device list: %w", err)
	}
	arr := env.Data
	if len(arr) == 0 || string(arr) == "null" {
		arr = env.Response
	}
	// device2 responses may nest again: {"response":{"data":[...]}}
	if len(arr) > 0 && arr[0] == '{' {
		var inner struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(arr, &inner); err == nil {
			arr = inner.Data
		}
	}
	if len(arr) == 0 || arr[0] != '[' {
		return nil, nil, fmt.Errorf("device list: unexpected envelope")
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(arr, &raws); err != nil {
		return nil, nil, fmt.Errorf("device list: %w", err)
	}
	out := make([]WireDevice, len(raws))
	for i, r := range raws {
		if err := json.Unmarshal(r, &out[i]); err != nil {
			return nil, nil, fmt.Errorf("device list[%d]: %w", i, err)
		}
	}
	return out, raws, nil
}

// DecodeSingleDevice decodes {"data":{...}} (v1 GET device).
func DecodeSingleDevice(body []byte) (WireDevice, json.RawMessage, error) {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	var d WireDevice
	if err := json.Unmarshal(body, &env); err != nil || len(env.Data) == 0 || env.Data[0] != '{' {
		return d, nil, fmt.Errorf("device: unexpected envelope")
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return d, nil, fmt.Errorf("device: %w", err)
	}
	return d, env.Data, nil
}

// StatesRequest is the body for PathDeviceStates. The key must be the
// plural "deviceIds" with a list; a singular key silently returns no data.
type StatesRequest struct {
	DeviceIDs []string `json:"deviceIds"`
}

// SetFieldsRequest is the body for PUT PathDeviceV1+id.
type SetFieldsRequest struct {
	Payload map[string]string `json:"payload"`
}

// DecodeAck extracts the write ack code from {"data":{"code":...}} or
// {"response":{"code":...}}.
func DecodeAck(body []byte) (string, error) {
	var env struct {
		Data *struct {
			Code string `json:"code"`
		} `json:"data"`
		Response *struct {
			Code string `json:"code"`
		} `json:"response"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("ack: %w", err)
	}
	switch {
	case env.Data != nil && env.Data.Code != "":
		return env.Data.Code, nil
	case env.Response != nil && env.Response.Code != "":
		return env.Response.Code, nil
	}
	return env.Code, nil
}

// DecodeProfile returns the profile object from {"data":{...}} or
// {"response":{...}}.
func DecodeProfile(body []byte) (map[string]any, error) {
	var env struct {
		Data     map[string]any `json:"data"`
		Response map[string]any `json:"response"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	if env.Data != nil {
		return env.Data, nil
	}
	if env.Response != nil {
		return env.Response, nil
	}
	return nil, fmt.Errorf("profile: unexpected envelope")
}

// WSRegister is the registerEvent message.
type WSRegister struct {
	Action string         `json:"action"`
	Body   WSRegisterBody `json:"body"`
}

// WSRegisterBody is the body of registerEvent.
type WSRegisterBody struct {
	DeviceIDs    string `json:"device_ids"`
	ActiveScreen string `json:"active_screen"`
	Token        string `json:"token"`
}

// WSEvent is an incoming push.
type WSEvent struct {
	EventType  string `json:"event_type"`
	ScreenType string `json:"screen_type"`
	DeviceID   string `json:"deviceId"`
	Data       struct {
		Fields map[string]WireField `json:"fields"`
		Online *bool                `json:"online"`
		Alarm  *bool                `json:"alarm"`
	} `json:"data"`
}

// Itoa is a small helper used by field writers.
func Itoa(n int) string { return strconv.Itoa(n) }
