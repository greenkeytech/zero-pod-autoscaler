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
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
)

// BiDiCopy copies bidirectional streams.
func BiDiCopy(a, b *net.TCPConn) error {
	errch1 := make(chan error)
	defer close(errch1)
	go func() {
		_, err := io.Copy(a, b)
		a.CloseWrite()
		b.CloseRead()
		errch1 <- err
	}()

	errch2 := make(chan error)
	defer close(errch2)
	go func() {
		_, err := io.Copy(b, a)
		b.CloseWrite()
		a.CloseRead()
		errch2 <- err
	}()

	err1, err2 := <-errch1, <-errch2

	// ignore connection resets because they happen all the time with docker client
	if errors.Is(err1, syscall.ECONNRESET) {
		err1 = nil
	}
	if errors.Is(err2, syscall.ECONNRESET) {
		err2 = nil
	}

	if err1 != nil && err2 != nil {
		return fmt.Errorf("failed to copy steams: %w; %w", err1, err2)
	}
	if err1 != nil {
		return fmt.Errorf("failed to copy steam: %w", err1)
	}
	if err2 != nil {
		return fmt.Errorf("failed to copy stream: %w", err2)
	}
	return nil
}

// ProxyTo dials a connection to remote then (bi-directionally) copies
// everything from src to the new connection.
func ProxyTo(src net.Conn, remote string) error {
	dst, err := net.Dial("tcp", remote)
	if err != nil {
		return err
	}
	defer dst.Close()

	_dst, ok := dst.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("dst impossibly not a tcp connection: %T", dst)
	}

	_src, ok := src.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("src not a tcp connection: %T", src)
	}

	return BiDiCopy(_dst, _src)
}
