#ifndef BLUETOOTH_BRIDGE_H
#define BLUETOOTH_BRIDGE_H

#include <stdint.h>

// Initialize the Bluetooth bridge
void initBluetooth();

// Start the RFCOMM listener
int startRFCOMMListener(const char* serviceName, const char* serviceUUID);

// Callback type for when a new connection is received
typedef void (*ConnectionCallback)(int channelID);

// Callback type for when data is received
typedef void (*DataCallback)(int channelID, uint8_t* data, int length);

// Set the callbacks
void setConnectionCallback(ConnectionCallback callback);
void setDataCallback(DataCallback callback);

// Write data to a channel
int writeToChannel(int channelID, const uint8_t* data, int length);

// Close a channel
void closeChannel(int channelID);

#endif
