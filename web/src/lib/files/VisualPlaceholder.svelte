<script>
  import { placeholderHue } from './visuals.ts'
  // Stands in for a preview image: cards posted before previews, or with no
  // browser to render them. It fills its parent box.
  let { title } = $props()
</script>

<div class="placeholder" style="--tint: var(--hue-{placeholderHue(title)})" aria-hidden="true">
  <span>{title}</span>
</div>

<style>
  .placeholder {
    width: 100%;
    height: 100%;
    display: flex;
    align-items: flex-end;
    padding: 18px 20px;
    background:
      radial-gradient(120% 140% at 0% 0%, color-mix(in srgb, var(--tint) 22%, transparent), transparent 60%),
      linear-gradient(135deg, var(--raise), var(--side));
  }
  span {
    font-size: 20px;
    font-weight: 600;
    color: var(--ink);
    line-height: 1.2;
    overflow: hidden;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
  }
</style>
