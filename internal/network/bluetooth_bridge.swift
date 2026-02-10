import Foundation
import IOBluetooth

@_cdecl("initBluetooth")
public func initBluetooth() {
    print("Swift: Initializing Bluetooth bridge")
}

var globalConnectionCallback: (@convention(c) (Int32) -> Void)?
var globalDataCallback: (@convention(c) (Int32, UnsafeMutablePointer<UInt8>, Int32) -> Void)?
var activeChannels: [Int32: IOBluetoothRFCOMMChannel] = [:]
var nextChannelID: Int32 = 1

@_cdecl("setConnectionCallback")
public func setConnectionCallback(callback: @escaping @convention(c) (Int32) -> Void) {
    globalConnectionCallback = callback
}

@_cdecl("setDataCallback")
public func setDataCallback(callback: @escaping @convention(c) (Int32, UnsafeMutablePointer<UInt8>, Int32) -> Void) {
    globalDataCallback = callback
}

@_cdecl("writeToChannel")
public func writeToChannel(channelID: Int32, data: UnsafePointer<UInt8>, length: Int32) -> Int32 {
    guard let channel = activeChannels[channelID] else { return -1 }
    // IOBluetooth channel.writeSync expects UnsafeMutableRawPointer
    let status = channel.writeSync(UnsafeMutableRawPointer(mutating: data), length: UInt16(length))
    return status == kIOReturnSuccess ? 0 : -1
}

@_cdecl("closeChannel")
public func closeChannel(channelID: Int32) {
    guard let channel = activeChannels.removeValue(forKey: channelID) else { return }
    channel.close()
}

class BluetoothDelegate: NSObject, IOBluetoothRFCOMMChannelDelegate {
    @objc func rfcommChannelOpen(notification: IOBluetoothUserNotification, channel: IOBluetoothRFCOMMChannel) {
        let id = nextChannelID
        nextChannelID += 1
        activeChannels[id] = channel
        channel.setDelegate(self)
        print("Swift: RFCOMM Channel opened with ID \(id)")
        globalConnectionCallback?(id)
    }

    func rfcommChannelOpenComplete(_ rfcommChannel: IOBluetoothRFCOMMChannel!, status: IOReturn) {
        // Not used for incoming channels registered via notification
    }

    func rfcommChannelData(_ rfcommChannel: IOBluetoothRFCOMMChannel!, data dataPointer: UnsafeMutableRawPointer!, length dataLength: Int) {
        guard let channelID = activeChannels.first(where: { $0.value == rfcommChannel })?.key else { return }
        let bytes = dataPointer.assumingMemoryBound(to: UInt8.self)
        globalDataCallback?(channelID, UnsafeMutablePointer(mutating: bytes), Int32(dataLength))
    }
    
    func rfcommChannelClosed(_ rfcommChannel: IOBluetoothRFCOMMChannel!) {
        guard let channelID = activeChannels.first(where: { $0.value == rfcommChannel })?.key else { return }
        print("Swift: RFCOMM Channel \(channelID) closed")
        activeChannels.removeValue(forKey: channelID)
    }
}

let delegate = BluetoothDelegate()

@_cdecl("startRFCOMMListener")
public func startRFCOMMListener(serviceName: UnsafePointer<Int8>, serviceUUID: UnsafePointer<Int8>) -> Int32 {
    let name = String(cString: serviceName)
    let uuidStr = String(cString: serviceUUID)
    
    // Convert UUID string to data
    let cleanUUID = uuidStr.replacingOccurrences(of: "-", with: "")
    var uuidBytes = [UInt8]()
    for i in stride(from: 0, to: cleanUUID.count, by: 2) {
        let start = cleanUUID.index(cleanUUID.startIndex, offsetBy: i)
        let end = cleanUUID.index(start, offsetBy: 2)
        if let byte = UInt8(cleanUUID[start..<end], radix: 16) {
            uuidBytes.append(byte)
        }
    }
    
    let uuid = IOBluetoothSDPUUID(bytes: uuidBytes, length: Int(uuidBytes.count))
    if uuid == nil {
        print("Swift: Failed to create IOBluetoothSDPUUID")
        return -1
    }
    
    print("Swift: Registering SDP service '\(name)' with UUID \(uuidStr)")
    
    let serviceDict: [String: Any] = [
        "0000*": name,
        "0001*": [uuid as Any],
        "0004*": [
            ["0001*"],
            ["0003*", 1]
        ]
    ]

    guard let record = IOBluetoothSDPServiceRecord.publishedServiceRecord(with: serviceDict) else {
        print("Swift: Failed to publish SDP record")
        return -1
    }
    
    print("Swift: SDP record published successfully")
    
    var rfcommChannelID: BluetoothRFCOMMChannelID = 0
    let res = record.getRFCOMMChannelID(&rfcommChannelID)
    if res == kIOReturnSuccess {
        print("Swift: Listening on RFCOMM Channel \(rfcommChannelID)")
        
        IOBluetoothRFCOMMChannel.register(forChannelOpenNotifications: delegate, selector: #selector(delegate.rfcommChannelOpen), withChannelID: rfcommChannelID, direction: kIOBluetoothUserNotificationChannelDirectionIncoming)
        print("Swift: Registered for channel open notifications")
    } else {
        print("Swift: Failed to get RFCOMM Channel ID from record (status: \(res))")
    }

    return 0
}
