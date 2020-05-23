#!/bin/bash
rm -rf build 
GO111MODULE=on CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -v -mod=vendor -ldflags="-s -w -X main.Version=${VERSION}" -a -o ./build/rtsp-proxy
