package api

// Field-name tables, keyed by device type. They are used for display and
// debugging only; decoders never depend on them. Sources: pypentair (IF31),
// homeassistant-pentair-intelliconnect (PIF0), hacs-pentair (PLC1).

import "fmt"

// FieldNames maps device type -> field code -> human name.
var FieldNames = map[string]map[string]string{
	"IF31": if31Names(),
	"PIF0": pif0Names,
	"PLC1": plc1Names,
}

// FieldName returns a display name for a code, or "".
func FieldName(deviceType, code string) string {
	return FieldNames[deviceType][code]
}

var programElements = map[int]string{
	1: "id", 2: "name", 3: "reference", 4: "value", 5: "type", 6: "start time of day",
	7: "duration", 8: "days to run", 9: "pump active", 10: "enable", 11: "relay 1",
	12: "relay 2", 13: "exists", 14: "relay 1 light mode", 15: "relay 2 light mode",
}

func if31Names() map[string]string {
	m := map[string]string{
		"s1":  "Device time",
		"s2":  "Finished good serial number",
		"s3":  "Drive type",
		"s4":  "Wet end type",
		"s5":  "Relay installed",
		"s6":  "Wifi mac address",
		"s7":  "Drive software version",
		"s8":  "IoT version",
		"s9":  "Controller version",
		"s10": "UDM application software version",
		"s11": "Security key",
		"s12": "Checksum",
		"s13": "RSSI",
		"s14": "Active program number",
		"s15": "Active reference",
		"s16": "Active value",
		"s17": "Current pressure",
		"s18": "Current power",
		"s19": "Current motor speed",
		"s20": "Alarm condition",
		"s21": "Relay 1 status",
		"s22": "Relay 2 status",
		"s23": "SSID",
		"s24": "Digital inputs",
		"s25": "Pump enabled status",
		"s26": "Current estimated flow",
		"s27": "Status word",
		"s28": "Remaining time",
		"s29": "Automation active",
		"s30": "Active program relay 1",
		"s31": "Active program relay 2",
		"s32": "Relay 1 remaining time",
		"s33": "Relay 2 remaining time",
		"s42": "Drive hardware version",
		"s43": "Comm board hardware version",
		"s44": "UDM assets software version",
		"s45": "UDM hardware version",
		"s46": "Drive flow table version",
		"s48": "Wifi level",
		"s50": "Feature bit",
		"d1":  "Pump automation address",
		"d2":  "Max speed",
		"d3":  "Max flow",
		"d4":  "Max pressure",
		"d5":  "Priming speed",
		"d6":  "Priming range",
		"d7":  "Max priming duration",
		"d8":  "Loss of prime",
		"d9":  "Time source",
		"d10": "Flow program pressure max",
		"d11": "Thermal mode enable",
		"d13": "Thermal mode temperature",
		"d14": "Password protection scheme",
		"d15": "Password",
		"d16": "Daylight savings",
		"d17": "Device time zone",
		"d18": "Min speed",
		"d19": "Min flow",
		"d20": "Flow limit speed",
		"d21": "Pressure limit speed",
		"d22": "Priming enabled",
		"d23": "Dry start",
		"d24": "Priming delay",
		"d25": "Pump start stop",
		"d26": "Date provisioned",
		"d27": "Pump nickname",
		"d28": "Relay 1 name",
		"d29": "Relay 1 flow dependent",
		"d30": "Relay 2 name",
		"d31": "Relay 2 flow dependent",
		"d32": "Ramp rate up",
		"d33": "Ramp rate down",
		"d34": "Device resets",
		"d35": "Ble always on",
		"d36": "Relay 1 type",
		"d37": "Relay 2 type",
		"d38": "Booster pump delay",
		"d42": "Timezone string",
		"p1":  "Setup complete flag",
		"p2":  "Last active program",
		"1":   "Pump type 1",
		"2":   "Pump type 2",
	}
	for p := 1; p <= 14; p++ {
		for e, n := range programElements {
			m[fmt.Sprintf("zp%de%d", p, e)] = fmt.Sprintf("Program %d %s", p, n)
		}
	}
	return m
}

var pif0Names = map[string]string{
	"ra0": "Relay1_Manual_Schedule", "ra1": "Relay1_Start_Time", "ra2": "Relay1_End_Time",
	"ra3": "Relay1_Timer", "ra4": "Relay1_Power", "ras0": "Relay1_Timer_Status",
	"p1": "Relay1_Egg_Timer", "htd1": "Heater_Mode", "htd2": "Heater_SetPoint",
	"htd13": "Heater_Aux_Flag", "htd14": "Heater_Cooldown", "icd1": "Chlor_Set_Percentage",
	"icd2": "Boost_Mode_RPM", "icd3": "Boost_Mode_Timer", "ics1": "Chlor_Percentage",
	"ics2": "Chlor_Salt_Value_PPM", "ics3": "Chlor_Error_Message", "ics9": "Chlor_Status_Call_Temp",
	"ics11": "Chlor_Status_Call_VerNum", "ics12": "Chlor_Device_Name", "ics13": "Chlor_BoostTimer_Status",
	"ics15": "Chlor_Operation_Hours", "t0": "Current_Water_Temp", "t1": "FP_Outside_Temp",
	"ifs3": "IntelliFlo_Power", "ifs4": "IntelliFlo_Speed", "ifs6": "IntelliFlo_Flow",
	"appuse": "App_In_Use",
}

var plc1Names = map[string]string{
	"d1":  "Light show / command",
	"d13": "Power",
}
