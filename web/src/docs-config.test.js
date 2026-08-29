import {afterEach, describe, expect, it} from 'vitest';
import {createSwaggerOptions} from './docs-config.js';

const intercept = (request, cookie = '') => {
  for (const value of cookie.split(';').map(part => part.trim()).filter(Boolean)) {
    document.cookie = `${value}; path=/`;
  }
  return createSwaggerOptions().requestInterceptor(request);
};

describe('Swagger documentation request configuration', () => {
  afterEach(() => {
    document.cookie = 'XSRF-TOKEN=; Max-Age=0; path=/';
    document.cookie = 'other=; Max-Age=0; path=/';
  });

  it('loads the bundled same-origin OpenAPI contract', () => {
    expect(createSwaggerOptions().url).toBe('/openapi.yaml');
  });

  it('always includes browser credentials', () => {
    expect(intercept({method: 'GET'}).credentials).toBe('include');
  });

  it.each(['GET', 'HEAD', 'OPTIONS'])('does not add CSRF for safe %s requests', method => {
    const request = intercept({method, headers: {}}, 'XSRF-TOKEN=safe-token');
    expect(request.headers).not.toHaveProperty('X-CSRF-Token');
  });

  it.each(['POST', 'PUT', 'PATCH', 'DELETE'])('copies XSRF-TOKEN into %s requests', method => {
    const request = intercept({method}, 'other=value; XSRF-TOKEN=token%20value');
    expect(request.headers['X-CSRF-Token']).toBe('token value');
    expect(request.credentials).toBe('include');
  });
});
