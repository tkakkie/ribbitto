//go:build lintfixture

package lintfixture

import "net/http"

// UnclosedResponse must fail bodyclose: checking status does not close the body.
func UnclosedResponse(client *http.Client, request *http.Request) (int, error) {
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	return response.StatusCode, nil
}
