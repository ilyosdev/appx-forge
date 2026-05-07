import { Redirect } from 'expo-router';

// Seed file — exists so Metro registers an inotify watcher on app/ at startup.
// For tab-based apps (the most common AI-generated layout): redirects to (tabs).
// For apps that generate their own app/index.tsx: overwritten during code push.
export default function Index() {
  return <Redirect href="/(tabs)" />;
}
