package tests

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"testing"
)

var srv *http.Server

func TestMain(m *testing.M) {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	srv := graphQLMockAPIServer()
	code := m.Run()
	srv.Close()
	os.Exit(code)
}

func graphQLMockAPIServer() *http.Server {
	srv = &http.Server{Addr: ":8081"}

	http.HandleFunc("/", graphQLAPIMock)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			panic(fmt.Sprintf("failed to start HTTP mock server with error=%s", err))
		}
	}()

	return srv
}
