// Appx sandbox entry point. Delegates to Expo Router so file-based routing
// picks up /app/code/app/**/*.tsx as the route graph. User-generated screens
// under app/(tabs)/, app/_layout.tsx, etc. render automatically.
//
// Previously this file registered a static fallback App.tsx that showed
// "Forge Sandbox Ready / Waiting for code push" — that placeholder stayed
// on screen even after the agent pushed the generated Expo Router tree,
// because Metro only evaluated this file's registerRootComponent() call.
import 'expo-router/entry';
