package testutil

// Synthetic field sets shaped like real devices (values invented; codes
// and scales from the reverse-engineering sources in docs/research.md).

// PumpFields is an IntelliFlo 3 running program 2 ("Cleaning").
func PumpFields() map[string]string {
	return map[string]string{
		"s13": "-61", "s14": "1", "s17": "2851", "s18": "1392", "s19": "725", "s20": "0",
		"s25": "1", "s26": "583", "s28": "3600", "d27": "Main Pump", "p2": "99",
		"zp1e2": "Filter", "zp1e5": "0", "zp1e10": "2", "zp1e13": "1", "zp1e4": "2400", "zp1e6": "480", "zp1e7": "480", "zp1e8": "127",
		"zp2e2": "Cleaning", "zp2e5": "2", "zp2e10": "3", "zp2e13": "1", "zp2e4": "3000",
		"zp3e2": "Spa", "zp3e5": "2", "zp3e10": "2", "zp3e13": "1",
		"zp4e2": "", "zp4e13": "0",
		"s6": "aa:bb:cc:dd:ee:ff", "s11": "secretkey", "s23": "HomeWifi", "d15": "1234",
	}
}

// PoolFields is an IntelliConnect with schedule enabled, pump running,
// heater in auto, and an IntelliChlor.
func PoolFields() map[string]string {
	return map[string]string{
		"ra0": "2", "ra1": "50400", "ra2": "79200", "ra3": "0", "ra4": "1392", "ras0": "0",
		"htd1": "1", "htd2": "840", "htd13": "1", "htd14": "300",
		"icd1": "40", "ics1": "40", "ics2": "3200", "ics3": "0", "ics9": "78", "ics11": "1.2",
		"ics12": "IC40", "ics13": "0", "ics15": "1234", "t0": "79", "t1": "-3",
	}
}

// LightFields is a Color Sync on, showing Caribbean.
func LightFields() map[string]string {
	return map[string]string{"d13": "1", "d1": "10"}
}
