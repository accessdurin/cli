import { z } from 'zod';

export const roleSchema = z.enum(['admin', 'security', 'member', 'auditor']);
export const environmentSchema = z.enum(['sandbox', 'production']);
export const countSchema = z.number().int().nonnegative();
