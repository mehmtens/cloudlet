import {defineConfig} from 'vite';
import react from '@vitejs/plugin-react';
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';

const openAPIPath = fileURLToPath(new URL('../openapi.yaml', import.meta.url));
const openAPI = () => readFileSync(openAPIPath, 'utf8');
const openAPIAsset = {
  name: 'cloudlet-openapi',
  configureServer(server) {
    server.middlewares.use('/openapi.yaml', (_request, response) => {
      response.setHeader('Content-Type', 'application/yaml; charset=utf-8');
      response.end(openAPI());
    });
  },
  generateBundle() {
    this.emitFile({type: 'asset', fileName: 'openapi.yaml', source: openAPI()});
  },
};

export default defineConfig({
  plugins: [react(), openAPIAsset],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:18080',
        rewrite: path => path.replace(/^\/api/, ''),
      },
    },
  },
  build: {
    sourcemap: false,
    rollupOptions: {
      input: {
        app: fileURLToPath(new URL('./index.html', import.meta.url)),
        docs: fileURLToPath(new URL('./docs/index.html', import.meta.url)),
      },
    },
  },
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
