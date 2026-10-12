.PHONY: all clean lint test app android

all: lint app

clean:
	@rm -rf build
	@rm -f android/app/src/main/jniLibs/arm64-v8a/libcronotopia.so
	@rm -f android/app/src/main/jniLibs/armeabi-v7a/libcronotopia.so

lint:
	@gofmt -w ./app

test:
	@go test -v ./test

app:
	@mkdir -p build
	@go build -ldflags "-s -w" -o build/cronotopia ./app

android:
	@mkdir -p android/app/src/main/jniLibs/arm64-v8a
	@mkdir -p android/app/src/main/jniLibs/armeabi-v7a
	@GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" -o android/app/src/main/jniLibs/arm64-v8a/libcronotopia.so ./app
	@GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "-s -w" -o android/app/src/main/jniLibs/armeabi-v7a/libcronotopia.so ./app
