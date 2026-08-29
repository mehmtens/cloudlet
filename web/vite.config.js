import {defineConfig} from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {port: 5173, proxy: {'/api': 'http://localhost:18080'}},
  build: {sourcemap: false},
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.{js,jsx}'],
    pool: 'threads',
    minWorkers: 1,
    maxWorkers: 1,
    fileParallelism: false,
    passWithNoTests: false,
    setupFiles: './src/test/setup.js',
    css: true,
  },
});
