package api

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
