const { getDefaultConfig } = require('expo/metro-config');

const config = getDefaultConfig(__dirname);

// Watch the code directory for file changes (bind-mounted by agent)
config.watchFolders = ['/app/code'];

// Resolve modules from both app and shared deps
config.resolver.nodeModulesPaths = [
  '/opt/expo-shared-deps/node_modules',
  '/app/app/node_modules',
];

module.exports = config;
