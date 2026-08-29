const csrfToken = () => document.cookie
  .split('; ')
  .find(value => value.startsWith('XSRF-TOKEN='))
  ?.split('=')
  .slice(1)
  .join('=') || '';

export function createSwaggerOptions() {
  return {
    url: '/openapi.yaml',
    dom_id: '#swagger-ui',
    deepLinking: true,
    displayRequestDuration: true,
    persistAuthorization: false,
    requestInterceptor(request) {
      request.credentials = 'include';
      const method = (request.method || 'GET').toUpperCase();
      const token = csrfToken();
      if (token && ['POST', 'PUT', 'PATCH', 'DELETE'].includes(method)) {
        request.headers = request.headers || {};
        request.headers['X-CSRF-Token'] = decodeURIComponent(token);
      }
      return request;
    },
  };
}
