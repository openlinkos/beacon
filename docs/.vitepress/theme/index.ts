import Theme from '@openlinkos/vitepress-theme';
import { OlBand, OlCast, OlHero, OlLogos, OlSteps } from '@openlinkos/vitepress-theme/components';
import './diagram.css';

// The landing-page components of the theme, registered for the site's Markdown (the home page uses
// them all). The spread keeps the theme's own enhanceApp (its interface pieces) working.
export default {
  ...Theme,
  enhanceApp(ctx) {
    for (const [name, component] of Object.entries({ OlBand, OlCast, OlHero, OlLogos, OlSteps })) {
      ctx.app.component(name, component);
    }
    Theme.enhanceApp?.(ctx);
  },
};
