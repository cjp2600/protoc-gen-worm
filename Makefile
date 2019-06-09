build-options:
	protoc -I/usr/local/include -I. \
	-I$(GOPATH)/src \
	-I$(GOPATH)/src/github.com/grpc-ecosystem/grpc-gateway/third_party/googleapis \
	--go_out=. \
	plugin/options/wgorm.proto

build:
	protoc -I/usr/local/include -I. \
	-I$(GOPATH)/src \
	-I$(GOPATH)/src/github.com/grpc-ecosystem/grpc-gateway/third_party/googleapis \
	--go_out=. \
	test.proto

	protoc -I/usr/local/include -I.  \
	-I$(GOPATH)/src   \
	-I$(GOPATH)/src/github.com/grpc-ecosystem/grpc-gateway/third_party/googleapis   \
	--plugin=protoc-gen-wgorm=app \
	--mongo_out="generateCrud=true,gateway:." \
 	test.proto