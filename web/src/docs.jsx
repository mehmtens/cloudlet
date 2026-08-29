import SwaggerUIBundle from 'swagger-ui-dist/swagger-ui-es-bundle.js';
import 'swagger-ui-dist/swagger-ui.css';
import {createSwaggerOptions} from './docs-config.js';

const root = document.getElementById('swagger-ui');
if (root) SwaggerUIBundle(createSwaggerOptions());
