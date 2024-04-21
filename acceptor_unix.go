// Copyright (c) 2021 The Gnet Authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux || freebsd || dragonfly || netbsd || openbsd || darwin
// +build linux freebsd dragonfly netbsd openbsd darwin

package gnet

import (
	"time"

	"golang.org/x/sys/unix"

	"github.com/panjf2000/gnet/v2/internal/netpoll"
	"github.com/panjf2000/gnet/v2/internal/queue"
	"github.com/panjf2000/gnet/v2/internal/socket"
	"github.com/panjf2000/gnet/v2/pkg/errors"
)

func (eng *engine) accept1(fd int, _ netpoll.IOEvent, _ netpoll.IOFlags) error {
	for {
		nfd, sa, err := socket.Accept(fd)
		if err != nil {
			switch err {
			case unix.EAGAIN: // the Accept queue has been drained, we can return now
				return nil
			case unix.EINTR, unix.ECONNRESET, unix.ECONNABORTED:
				// ECONNRESET or ECONNABORTED could indicate that a socket
				// in the Accept queue was closed before we Accept()ed it.
				// It's a silly error, let's retry it.
				continue
			default:
				eng.opts.Logger.Errorf("Accept() failed due to error: %v", err)
				return errors.ErrAcceptSocket
			}
		}

		remoteAddr := socket.SockaddrToTCPOrUnixAddr(sa)
		if eng.opts.TCPKeepAlive > 0 && eng.listeners[fd].network == "tcp" {
			err = socket.SetKeepAlivePeriod(nfd, int(eng.opts.TCPKeepAlive.Seconds()))
			if err != nil {
				eng.opts.Logger.Errorf("failed to set TCP keepalive on fd=%d: %v", fd, err)
			}
		}

		el := eng.eventLoops.next(remoteAddr)
		c := newTCPConn(nfd, el, sa, el.listeners[fd].addr, remoteAddr)
		err = el.poller.Trigger(queue.HighPriority, el.register, c)
		if err != nil {
			eng.opts.Logger.Errorf("failed to enqueue accepted socket of high-priority: %v", err)
			_ = unix.Close(nfd)
			c.release()
		}
	}
}

func (el *eventloop) accept1(fd int, ev netpoll.IOEvent, flags netpoll.IOFlags) error {
	if el.listeners[fd].network == "udp" {
		return el.readUDP1(fd, ev, flags)
	}

	nfd, sa, err := socket.Accept(fd)
	if err != nil {
		switch err {
		case unix.EINTR, unix.EAGAIN, unix.ECONNRESET, unix.ECONNABORTED:
			// ECONNRESET or ECONNABORTED could indicate that a socket
			// in the Accept queue was closed before we Accept()ed it.
			// It's a silly error, let's retry it.
			return nil
		default:
			el.getLogger().Errorf("Accept() failed due to error: %v", err)
			return errors.ErrAcceptSocket
		}
	}

	remoteAddr := socket.SockaddrToTCPOrUnixAddr(sa)
	if el.engine.opts.TCPKeepAlive > 0 && el.listeners[fd].network == "tcp" {
		err = socket.SetKeepAlivePeriod(nfd, int(el.engine.opts.TCPKeepAlive/time.Second))
		if err != nil {
			el.getLogger().Errorf("failed to set TCP keepalive on fd=%d: %v", fd, err)
		}
	}

	c := newTCPConn(nfd, el, sa, el.listeners[fd].addr, remoteAddr)
	addEvents := el.poller.AddRead
	if el.engine.opts.EdgeTriggeredIO {
		addEvents = el.poller.AddReadWrite
	}
	if err = addEvents(&c.pollAttachment, el.engine.opts.EdgeTriggeredIO); err != nil {
		return err
	}
	el.connections.addConn(c, el.idx)
	return el.open(c)
}
