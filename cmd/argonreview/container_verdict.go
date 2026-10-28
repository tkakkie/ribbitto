package main

func commandVerdict(code int, err error, truncated bool) string {
	if code != 0 || err != nil {
		return "FAILED"
	}
	if truncated {
		return "NOT VERIFIED: output limit exceeded (output cut off)"
	}
	return "verified"
}

type containerTestEvent struct {
	Action, Package, Test, Output, ImportPath string
}
