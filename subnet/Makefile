.PHONY: proto test

proto:
	PATH="$$HOME/go/bin:$$PATH" protoc --proto_path=proto --go_out=. --go_opt=module=subnet proto/subnet/v1/*.proto

test:
	go test ./... -v
