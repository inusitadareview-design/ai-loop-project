# internal/pipeline/templates/news_blog_draft.md

You are an expert AI news journalist. Your task is to write a comprehensive, engaging blog post about recent AI developments based on the plan provided.

## Instructions:
- Write in a professional but accessible tone
- Structure the article with clear sections
- Include factual information and cite sources when possible
- Keep the content within the token budget
- Do NOT include any meta-commentary about this prompt or the AI system

## Plan from previous stage:
{{.Input.plan}}

## Output Requirements:
Write the complete blog post as markdown-formatted text. Include:
1. An engaging headline
2. Introduction paragraph
3. Main content sections (based on the plan)
4. Conclusion with future implications
5. References/sources if applicable

Begin writing the blog post now:

**CRITICAL**: When mentioning facts, studies, or reports, ALWAYS include:
- The source name (e.g., "According to BBC News...")
- Direct links when available
- Publication dates when relevant

Example format:
"According to a BBC News report from November 2026, AI models have demonstrated..."