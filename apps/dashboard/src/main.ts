import { createApp } from 'vue';
import App from './App.vue';
import { createAppRouter } from './router';
import { createAuthSession } from './api/auth';
import './style.css';

const session = createAuthSession();
createApp(App, { session }).use(createAppRouter()).mount('#app');
