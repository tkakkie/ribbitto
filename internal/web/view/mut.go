package view

func mutScope(drop bool) []submitValue {
	if drop {
		return nil
	}
	return []submitValue{{"scope", "all"}}
}
