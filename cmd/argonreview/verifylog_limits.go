package main

const (
	verifyLogCaptures  = 4
	verifyLogRecordMax = aggregatorOutputLimit + aggregatorStderrLimit + aggregatorNameLimit +
		aggregatorStdoutLimit + aggregatorBaseCharge + aggregatorBrowserCharge
	verifyLogStoreBytes    = verifyLogCaptures * verifyLogRecordMax
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
