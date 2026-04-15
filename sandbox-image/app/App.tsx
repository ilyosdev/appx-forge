import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import { StatusBar } from 'expo-status-bar';

export default function App() {
  return (
    <View style={styles.container}>
      <Text style={styles.text}>Forge Sandbox Ready</Text>
      <Text style={styles.subtext}>Waiting for code push...</Text>
      <StatusBar style="auto" />
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: '#1a1a2e',
  },
  text: {
    fontSize: 24,
    fontWeight: 'bold',
    color: '#e0e0e0',
  },
  subtext: {
    fontSize: 14,
    color: '#888',
    marginTop: 8,
  },
});
