package main

const (
	verifyLogCaptures  = 4
	verifyLogRecordMax = aggregatorOutputLimit + aggregatorStderrLimit + aggregatorNameLimit +
		aggregatorStdoutLimit + aggregatorBaseCharge + aggregatorBrowserCharge
	// R_max omits derived displays/slices/maps, name allocation rounding,
	// marker lines, browser identities and trusted allowed-name storage.
	// Per entry: rounded display, 32-byte index slot, map slot, three rounded
	// identities, and an allowed-name slot plus rounding. The four auxiliary
	// map slots and ID are fixed. Marker lines and browser names use #1850 caps.
	verifyLogDerivedAllowance = aggregatorIndexLimit*((summaryLineLimit+aggregatorAllocationQuantum-1)/aggregatorAllocationQuantum*aggregatorAllocationQuantum+
		2*aggregatorNameSlotCharge+aggregatorSlotCharge+3*aggregatorAllocationQuantum+
		aggregatorNameSlotCharge+aggregatorAllocationQuantum) + 4*aggregatorSlotCharge + verifyLogIDBytes*2 +
		2*aggregatorLineLimit + (browserLeafLimit+3)*summaryIdentityLimit + len(containerMeasurementSkips)
	verifyLogCaptureMax    = verifyLogRecordMax + verifyLogDerivedAllowance
	verifyLogStoreBytes    = verifyLogCaptures * verifyLogCaptureMax
	verifyLogResponseBytes = 6144
	verifyLogPageLines     = 80
	verifyLogDataBytes     = 512
	verifyLogPrefixBytes   = 16 << 10
	verifyLogIndexReads    = 16
	verifyLogEntryReads    = 40
	verifyLogAuxReads      = 8
	verifyLogServerReads   = 256
	verifyLogServerBytes   = 1 << 20
	verifyLogNameBytes     = 256
	verifyLogOrdinalDigits = 5
	verifyLogStartDigits   = 7
	verifyLogIDBytes       = 16
	verifyLogAuditIDDigits = 8
)
