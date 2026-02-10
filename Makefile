build-swift:
	swiftc -emit-library -static -o internal/network/libbluetooth_bridge.a internal/network/bluetooth_bridge.swift -Xfrontend -disable-objc-attr-requires-foundation-module
