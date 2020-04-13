// Copyright 2020 GreenKey Technologies
//
// Licensed under the Apache License, Version 2.0 (the "License"); you
// may not use this file except in compliance with the License.  You
// may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.  See the License for the specific language governing
// permissions and limitations under the License.
package proxy

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func proxyOneConnection(t *testing.T, target string) string {
	addr := "127.0.0.1:"
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("failed to listen on %s: %v", addr, err)
	}

	go func() {
		defer ln.Close()

		conn, err := ln.Accept()
		if err != nil {
			t.Fatalf("failed to accept connection: %w", err)
		}

		defer conn.Close()

		if err := ProxyTo(conn, target); err != nil {
			t.Fatalf("failed to proxy: %v", err)
		}
	}()

	return ln.Addr().String()
}

type OkHandler struct {
	Body string
}

func (h OkHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Write([]byte(h.Body))
}

func TestProxyTo(t *testing.T) {
	handler := OkHandler{"this is the response body"}
	server := httptest.NewServer(handler)
	proxyAddr := proxyOneConnection(t, server.Listener.Addr().String())

	resp, err := http.Get(fmt.Sprintf("http://%s/", proxyAddr))
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if string(body) != handler.Body {
		t.Fatalf("response body did not match: expected: %q got: %q", handler.Body, body)
	}
}
