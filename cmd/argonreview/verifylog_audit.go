package main

import "fmt"

func verifyLogAudit(a verifyLogArguments, r verifyLogReply) string {
	id, stream := "-", "-"
	if verifyLogRunID(a.run) {
		id = a.run[:min(len(a.run), verifyLogAuditIDDigits)]
	}
	if verifyLogStream(a.stream) >= 0 {
		stream = a.stream
	}
	return fmt.Sprintf("verify-log run=%s stream=%s range=%d-%d bytes=%d class=%s detections=%d names=%d\n", id, stream, r.start, r.end, len(r.line), r.class, r.detections, r.nameDetections)
}
