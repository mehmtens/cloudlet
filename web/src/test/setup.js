import '@testing-library/jest-dom/vitest';
import {afterEach} from 'vitest';
import {cleanup} from '@testing-library/react';

afterEach(() => {
  cleanup();
  document.cookie = 'XSRF-TOKEN=; Max-Age=0; path=/';
});
